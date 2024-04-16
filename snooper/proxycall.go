package snooper

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type proxyCallContext struct {
	callIndex    uint64
	context      context.Context
	cancelFn     context.CancelFunc
	cancelled    bool
	deadline     time.Time
	updateChan   chan time.Duration
	streamReader io.ReadCloser
}

func (s *Snooper) newProxyCallContext(parent context.Context, timeout time.Duration) *proxyCallContext {
	s.callIndexMutex.Lock()
	s.callIndexCounter++
	callIndex := s.callIndexCounter
	s.callIndexMutex.Unlock()

	callCtx := &proxyCallContext{
		callIndex:  callIndex,
		deadline:   time.Now().Add(timeout),
		updateChan: make(chan time.Duration, 5),
	}
	callCtx.context, callCtx.cancelFn = context.WithCancel(parent)
	go callCtx.processCallContext()
	return callCtx
}

func (callContext *proxyCallContext) processCallContext() {
ctxLoop:
	for {
		timeout := time.Until(callContext.deadline)
		select {
		case newTimeout := <-callContext.updateChan:
			callContext.deadline = time.Now().Add(newTimeout)
		case <-callContext.context.Done():
			break ctxLoop
		case <-time.After(timeout):
			callContext.cancelFn()
			callContext.cancelled = true
			time.Sleep(10 * time.Millisecond)
		}
	}
	callContext.cancelled = true
	if callContext.streamReader != nil {
		callContext.streamReader.Close()
	}
}

func (s *Snooper) processProxyCall(w http.ResponseWriter, r *http.Request) error {
	callContext := s.newProxyCallContext(r.Context(), s.CallTimeout)
	defer callContext.cancelFn()

	// pass all headers
	hh := http.Header{}
	for hk, hvs := range r.Header {
		for _, hv := range hvs {
			hh.Add(hk, hv)
		}
	}

	proxyIpChain := []string{}
	if forwaredFor := r.Header.Get("X-Forwarded-For"); forwaredFor != "" {
		proxyIpChain = strings.Split(forwaredFor, ", ")
	}
	proxyIpChain = append(proxyIpChain, r.RemoteAddr)
	hh.Set("X-Forwarded-For", strings.Join(proxyIpChain, ", "))

	// build proxy url
	queryArgs := ""
	if r.URL.RawQuery != "" {
		queryArgs = fmt.Sprintf("?%s", r.URL.RawQuery)
	}
	proxyUrl, err := url.Parse(fmt.Sprintf("%s%s%s", s.target, r.URL.EscapedPath(), queryArgs))
	if err != nil {
		return fmt.Errorf("error parsing proxy url: %w", err)
	}

	// read body
	reqBodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("error reading request body: %w", err)
	}

	reqRawBodyBytes := reqBodyBytes
	if r.Header.Get("Content-Encoding") == "gzip" {
		buf := bytes.NewBuffer(reqBodyBytes)
		reader, err := gzip.NewReader(buf)
		if err != nil {
			return fmt.Errorf("failed unpacking gzip request body: %v", err)
		}
		defer reader.Close()

		reqRawBodyBytes, err = io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("failed unpacking gzip request body: %v", err)
		}
	}

	s.logRequest(callContext, r, reqRawBodyBytes)

	// construct request to send to origin server
	req := &http.Request{
		Method:        r.Method,
		URL:           proxyUrl,
		Header:        hh,
		Body:          io.NopCloser(bytes.NewReader(reqBodyBytes)),
		ContentLength: r.ContentLength,
		Close:         r.Close,
	}
	client := &http.Client{Timeout: 0}
	req = req.WithContext(callContext.context)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("proxy request error: %w", err)
	}
	if callContext.cancelled {
		resp.Body.Close()
		return fmt.Errorf("proxy context cancelled")
	}
	callContext.streamReader = resp.Body

	respContentType := resp.Header.Get("Content-Type")
	isEventStream := respContentType == "text/event-stream" || strings.HasPrefix(r.URL.EscapedPath(), "/eth/v1/events")

	// passthru response headers
	respH := w.Header()
	for hk, hvs := range resp.Header {
		for _, hv := range hvs {
			respH.Add(hk, hv)
		}
	}

	if isEventStream {
		respH.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)

	if isEventStream && resp.StatusCode == 200 {
		callContext.updateChan <- s.CallTimeout
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		_, err := s.processEventStreamResponse(callContext, r, w, resp)
		if err != nil {
			s.logger.WithField("callidx", callContext.callIndex).Warnf("event stream error: %v", err)
		}
	} else {
		// read response body
		rspBodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("error reading response body: %w", err)
		}

		rspRawBodyBytes := rspBodyBytes
		if resp.Header.Get("Content-Encoding") == "gzip" {
			buf := bytes.NewBuffer(rspBodyBytes)
			reader, err := gzip.NewReader(buf)
			if err != nil {
				return fmt.Errorf("failed unpacking gzip response body: %v", err)
			}
			defer reader.Close()

			rspRawBodyBytes, err = io.ReadAll(reader)
			if err != nil {
				return fmt.Errorf("failed unpacking gzip response body: %v", err)
			}
		}

		s.logResponse(callContext, r, resp, rspRawBodyBytes)

		_, err = w.Write(rspBodyBytes)
		if err != nil {
			return fmt.Errorf("proxy response stream error: %w", err)
		}
	}

	return nil
}

func (s *Snooper) processEventStreamResponse(callContext *proxyCallContext, r *http.Request, w http.ResponseWriter, rsp *http.Response) (int64, error) {
	rd := bufio.NewReader(rsp.Body)
	written := int64(0)

	for {
		lineBuf := []byte{}
		for {
			evt, err := rd.ReadSlice('\n')
			if err != nil {
				return written, err
			}
			wb, err := w.Write(evt)
			if err != nil {
				return written, err
			}
			written += int64(wb)
			if wb == 1 {
				break
			}
			lineBuf = append(lineBuf, evt...)
			lineBuf = append(lineBuf, '\n')
		}
		if len(lineBuf) > 2 {
			s.logEventResponse(callContext, r, rsp, lineBuf)
		}

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if callContext.cancelled {
			return written, nil
		}

		callContext.updateChan <- s.CallTimeout
	}
}
