package server_sdk

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"
	"wordofwisdom/pkg/protocol"
)

type ServerSDK struct {
	serverAddress       string
	maxMessageSizeBytes int
	popMessageTimeout   time.Duration

	ctx  context.Context
	conn net.Conn

	messagesCh  chan []byte
	connCloseCh chan error
	errCh       chan error

	closed atomic.Bool
}

func NewServerSDK(
	ctx context.Context,
	address string,
	maxMessageSizeBytes int,
	popMessageTimeout time.Duration,
) *ServerSDK {
	return &ServerSDK{
		serverAddress:       address,
		ctx:                 ctx,
		maxMessageSizeBytes: maxMessageSizeBytes,
		popMessageTimeout:   popMessageTimeout,
		messagesCh:          make(chan []byte),
		connCloseCh:         make(chan error),
		errCh:               make(chan error),
	}
}

var (
	ErrConnectionClosed     = errors.New("connection closed")
	ErrConnectionFailed     = errors.New("connection failed")
	ErrMessageTooShort      = errors.New("message is too short")
	ErrFailedToWaitMessage  = errors.New("failed to wait message")
	ErrFailedToSendMessage  = errors.New("failed to send message")
	ErrFailedToBuildMessage = errors.New("failed to build message")
	ErrPopMessageTimeout    = errors.New("pop message timeout")
)

func (s *ServerSDK) OpenConnection() error {
	conn, err := net.Dial("tcp", s.serverAddress)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			return ErrConnectionClosed
		}
		return errors.Join(err, ErrConnectionFailed)
	}
	s.conn = conn

	go s.startReceivingMessages()

	return nil
}

func (s *ServerSDK) startReceivingMessages() {
	messageBuff := make([]byte, s.maxMessageSizeBytes)

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		bytesMessage, err := s.conn.Read(messageBuff)
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.connCloseCh <- ErrConnectionClosed
				s.errCh <- err

				s.closed.Store(true)
				close(s.connCloseCh)
				close(s.errCh)
				close(s.messagesCh)

				return
			}
			s.errCh <- errors.Join(err, ErrFailedToWaitMessage)
			continue
		}

		log.Printf("Received message from server, %d bytes", bytesMessage)

		exact := make([]byte, bytesMessage)
		copy(exact, messageBuff[:bytesMessage])

		s.messagesCh <- exact
	}
}

func (s *ServerSDK) CloseConnection() error {
	return s.conn.Close()
}

func (s *ServerSDK) WaitForClose() error {
	return <-s.connCloseCh
}

func (s *ServerSDK) SendMessage(success bool, opcode uint32, payload protocol.MessageEncoder) error {
	rawMessage, err := protocol.BuildRawMessage(success, opcode, payload)
	if err != nil {
		return errors.Join(err, ErrFailedToBuildMessage)
	}

	_, err = s.conn.Write(rawMessage)
	if err != nil {
		return errors.Join(err, ErrFailedToSendMessage)
	}

	return nil
}

func (s *ServerSDK) PopMessage() (*protocol.RawMessage, error) {
	if s.closed.Load() {
		return nil, ErrConnectionClosed
	}
	timeout := time.After(s.popMessageTimeout)

	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-timeout:
		return nil, ErrPopMessageTimeout
	case message := <-s.messagesCh:
		return protocol.ParseRawMessage(message)

	case err := <-s.errCh:
		return nil, errors.Join(err, ErrFailedToWaitMessage)
	}
}
