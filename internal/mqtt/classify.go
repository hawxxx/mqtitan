package mqtt

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	v3packets "github.com/eclipse/paho.mqtt.golang/packets"
)

// Connect failure stages. Every failed connect classifies into exactly one.
const (
	StageDNS               = "dns"
	StageTCP               = "tcp"
	StageTLSHandshake      = "tls_handshake"
	StageTLSVerifyChain    = "tls_verify_chain"
	StageTLSVerifyHostname = "tls_verify_hostname"
	StageTLSAlert          = "tls_alert"
	StageWebsocket         = "websocket_upgrade"
	StageConnack           = "mqtt_connack"
	StageTimeout           = "timeout"
	StageOther             = "other"
)

// StageError tags an error with the connect stage that produced it.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// ConnackError reports a CONNACK refusal; Code is the MQTT 3.1.1 return code
// or the MQTT 5 reason code.
type ConnackError struct{ Code byte }

func (e *ConnackError) Error() string { return fmt.Sprintf("CONNACK reason %d", e.Code) }

// Failure is a classified connect failure. Code is -1 unless Stage is StageConnack.
type Failure struct {
	Stage string
	Code  int
}

// Classify maps a connect error to its failure stage. Order matters: a DNS
// stage tag wins over timeouts (a resolver that timed out is a DNS failure),
// timeouts win over every other stage, and explicit causes win over the
// coarse stage tag recorded at the point of failure.
func Classify(err error) Failure {
	f := func(stage string) Failure { return Failure{stage, -1} }
	var se *StageError
	hasStage := errors.As(err, &se)
	if hasStage && se.Stage == StageDNS {
		return f(StageDNS)
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return f(StageTimeout)
	}
	var chain *ChainError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &chain) || errors.As(err, &unknown) || errors.As(err, &invalid) {
		return f(StageTLSVerifyChain)
	}
	var mismatch *HostnameMismatchError
	var host x509.HostnameError
	if errors.As(err, &mismatch) || errors.As(err, &host) {
		return f(StageTLSVerifyHostname)
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "remote error" {
		return f(StageTLSAlert)
	}
	var ack *ConnackError
	if errors.As(err, &ack) {
		return Failure{StageConnack, int(ack.Code)}
	}
	for code := byte(1); code <= 5; code++ {
		if errors.Is(err, v3packets.ConnErrors[code]) {
			return Failure{StageConnack, int(code)}
		}
	}
	if hasStage {
		return f(se.Stage)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return f(StageDNS)
	}
	if op != nil && op.Op == "dial" {
		return f(StageTCP)
	}
	return f(StageOther)
}

// alertDrain bounds the read that looks for a pending TLS alert after a write failure.
const alertDrain = 100 * time.Millisecond

// alertConn remembers a remote TLS alert. A TLS 1.3 server rejects a client
// certificate after the client handshake completes, so the alert surfaces on
// the first read inside the MQTT library, where library wrapping hides it.
type alertConn struct {
	net.Conn
	alert atomic.Pointer[error]
}

func (c *alertConn) note(err error) {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "remote error" {
		c.alert.CompareAndSwap(nil, &err)
	}
}

func (c *alertConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		c.note(err)
	}
	return n, err
}

// Write surfaces a pending alert when the server already reset the connection:
// the library closes the socket on a failed write, which would discard the alert.
func (c *alertConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err != nil && c.alert.Load() == nil {
		_ = c.Conn.SetReadDeadline(time.Now().Add(alertDrain))
		_, rerr := c.Conn.Read(make([]byte, 1))
		c.note(rerr)
	}
	return n, err
}

// alertOr returns the recorded remote TLS alert if there is one, else err.
func (c *alertConn) alertOr(err error) error {
	if c != nil {
		if p := c.alert.Load(); p != nil {
			return *p
		}
	}
	return err
}
