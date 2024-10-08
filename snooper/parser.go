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

func BuildLogEntry(req *http.Request, resp *http.Response, duration time.Duration) (*logEntry, error) {
	// decode json rpc body
	jrpcReq, err := ParseJSONRPCRequest(req.Body)
	if err != nil {
		return nil, err
	}

	return &logEntry{
		server:        req.Host,
		scheme:        req.URL.Scheme,
		method:        req.Method,
		hostname:      req.Host,
		status:        strconv.Itoa(resp.StatusCode),
		protocol:      req.Proto,
		uri:           req.URL.String(),
		jrpcMethod:    jrpcReq.Method,
		clientIP:      net.ParseIP(req.RemoteAddr),
		duration:      duration.Seconds(),
		bytesSent:     uint64(resp.ContentLength),
		bytesReceived: uint64(resp.ContentLength),
	}, nil
}
