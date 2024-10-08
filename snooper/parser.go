package snooper

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

type logEntry struct {
	// Tags
	server     string
	scheme     string
	method     string
	hostname   string
	status     string
	protocol   string
	uri        string
	jrpcMethod string

	// Fields
	clientIP      net.IP
	duration      float64
	bytesSent     uint64
	bytesReceived uint64
}

type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      interface{} `json:"id"`
}

func ParseJSONRPCRequest(body io.Reader) (*JSONRPCRequest, error) {
	var req JSONRPCRequest
	decoder := json.NewDecoder(body)
	err := decoder.Decode(&req)
	if err != nil {
		return nil, err
	}
	return &req, nil
}

func BuildLogEntry(req *http.Request, resp *http.Response, duration time.Duration, jrpcMethod string) (*logEntry, error) {
	bytesSent := uint64(0)
	if req.ContentLength > 0 {
		bytesSent = uint64(req.ContentLength)
	}

	bytesReceived := uint64(0)
	if resp.ContentLength > 0 {
		bytesReceived = uint64(resp.ContentLength)
	}

	return &logEntry{
		server:        req.Host,
		scheme:        req.URL.Scheme,
		method:        req.Method,
		hostname:      req.Host,
		status:        strconv.Itoa(resp.StatusCode),
		protocol:      req.Proto,
		uri:           req.URL.String(),
		jrpcMethod:    jrpcMethod,
		clientIP:      net.ParseIP(req.RemoteAddr),
		duration:      duration.Seconds(),
		bytesSent:     bytesSent,
		bytesReceived: bytesReceived,
	}, nil
}
