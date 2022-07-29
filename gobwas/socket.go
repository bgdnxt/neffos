package gobwas

import (
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/kataras/neffos"

	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Socket completes the `neffos.Socket` interface,
// it describes the underline websocket connection.
type Socket struct {
	UnderlyingConn net.Conn
	request        *http.Request

	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	state          gobwas.State

	mu sync.Mutex

	//after idleTime not received any MESSAGE/PING/PONG
	//will send a PING to remote. keepalive
	//set zero will disable this feature
	pingPeriod time.Duration
}

func newSocket(underline net.Conn, request *http.Request, client bool, pingPeriod time.Duration) *Socket {
	state := gobwas.StateServerSide
	if client {
		state = gobwas.StateClientSide
	}

	controlHandler := wsutil.ControlFrameHandler(underline, state)

	reader := &wsutil.Reader{
		Source:          underline,
		State:           state,
		CheckUTF8:       true,
		SkipHeaderCheck: false,
		// "intermediate" frames, that possibly could
		// be received between text/binary continuation frames.
		// Read `gobwas/wsutil/reader#NextReader`.
		//
		OnIntermediate: controlHandler,
	}

	return &Socket{
		UnderlyingConn: underline,
		request:        request,
		state:          state,
		reader:         reader,
		controlHandler: controlHandler,
		pingPeriod:     pingPeriod,
	}
}

// NetConn returns the underline net connection.
func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn
}

// Request returns the http request value.
func (s *Socket) Request() *http.Request {
	return s.request
}

// ReadData reads binary or text messages from the remote connection.
func (s *Socket) ReadData(timeout time.Duration) ([]byte, neffos.MessageType, error) {
	delayTime := s.pingPeriod
	var delayTimer *time.Timer
	for {
		if timeout > 0 {
			s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
		}

		if delayTime > 0 {
			delayTimer = time.AfterFunc(delayTime, func() {
				s.WriteOp([]byte(""), gobwas.OpPing, delayTime)
			})
		}

		hdr, err := s.reader.NextFrame()

		if delayTimer != nil {
			delayTimer.Stop()
			delayTimer = nil
		}

		if err != nil {
			if err == io.EOF {
				return nil, 0, io.ErrUnexpectedEOF // for io.ReadAll to return an error if connection remotely closed.
			}
			return nil, 0, err
		}

		if hdr.OpCode == gobwas.OpClose {
			return nil, 0, io.ErrUnexpectedEOF // for io.ReadAll to return an error if connection remotely closed.
		}

		if hdr.OpCode.IsControl() {
			err = s.controlHandler(hdr, s.reader)
			if err != nil {
				return nil, 0, err
			}
			continue
		}

		if hdr.OpCode&gobwas.OpBinary == 0 && hdr.OpCode&gobwas.OpText == 0 {
			err = s.reader.Discard()
			if err != nil {
				return nil, 0, err
			}
			continue
		}

		b, err := ioutil.ReadAll(s.reader)
		if err != nil {
			return nil, 0, err
		}

		return b, neffos.MessageType(hdr.OpCode), nil
	}

	// for {
	// 	if timeout > 0 {
	// 		s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
	// 	}

	// 	b, code, err := wsutil.ReadData(s.UnderlyingConn, s.state)
	// 	if err != nil {
	// 		return nil, err
	// 	}

	// 	if code != defaultOp {
	// 		continue
	// 	}

	// 	return b, nil
	// }
}

// WriteBinary sends a binary message to the remote connection.
func (s *Socket) WriteBinary(body []byte, timeout time.Duration) error {
	return s.write(body, gobwas.OpBinary, timeout)
}

// WriteText sends a text message to the remote connection.
func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	return s.write(body, gobwas.OpText, timeout)
}

func (s *Socket) WriteOp(body []byte, op gobwas.OpCode, timeout time.Duration) error {
	return s.write(body, op, timeout)
}

func (s *Socket) write(body []byte, op gobwas.OpCode, timeout time.Duration) error {
	s.mu.Lock()
	if timeout > 0 {
		s.UnderlyingConn.SetWriteDeadline(time.Now().Add(timeout))
	}

	// println("write: " + string(body))
	err := wsutil.WriteMessage(s.UnderlyingConn, s.state, op, body)
	s.mu.Unlock()

	return err
}
