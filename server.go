package gotcp

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"
)

type Server struct {
	lis          net.Listener
	maxCount     int
	currentCount int

	pc   chan *Package
	done chan struct{}
}

func NewServer(listener net.Listener) *Server {
	return &Server{
		lis:  listener,
		pc:   make(chan *Package, 1000),
		done: make(chan struct{}, 1),
	}
}

func (s *Server) ListenAndServe(transport TransportInterface, reader ReaderInterface) {
	go s.parsePackage(transport)

	var maxDelay = time.Second
	var tempDelay time.Duration
	for {
		conn, err := s.lis.Accept()
		if err != nil {
			if ne, ok := err.(interface {
				Temporary() bool
			}); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}

				if tempDelay > maxDelay {
					tempDelay = maxDelay
				}

				time.Sleep(tempDelay)
				continue
			}

			return
		}

		tempDelay = 0

		if !s.isThrottling() {
			_ = conn.Close()
			continue
		}

		s.currentCount++

		go func() {
			s.handleConn(conn, reader)
			s.delConn(conn, transport)
		}()
	}
}

func (s *Server) delConn(conn net.Conn, transport TransportInterface) {
	transport.Clear(conn)
	_ = conn.Close()
	s.currentCount--
}

func (s *Server) SetMaxCount(cnt int) {
	s.maxCount = cnt
}

func (s *Server) isThrottling() bool {
	if s.maxCount <= 0 {
		return true
	}
	if s.maxCount < s.currentCount {
		return false
	}
	return true
}

func (s *Server) handleConn(conn net.Conn, reader ReaderInterface) {
	if err := conn.SetDeadline(time.Now().Add(reader.Timeout())); err != nil {
		reader.HandleError(fmt.Errorf("conn set deadline failed %w", err))
		return
	}

	for {
		var hb = make([]byte, reader.HeaderSize())
		n, err := io.ReadFull(conn, hb)
		if err != nil {
			break
		}
		h, err := reader.NewHeader(hb[:n])
		if err != nil {
			reader.HandleError(fmt.Errorf("new header failed %w", err))
			break
		}

		buff := make([]byte, h.GetSize())
		_, err = io.ReadFull(conn, buff)
		if err != nil {
			reader.HandleError(fmt.Errorf("io read full failed %w", err))
			break
		}

		select {
		case s.pc <- newPackage(conn, h.GetCmd(), buff):
			if err = conn.SetDeadline(time.Now().Add(reader.Timeout())); err != nil {
				reader.HandleError(fmt.Errorf("conn set deadline failed %w", err))
			}

		case <-time.After(reader.Timeout()):
			reader.HandleError(fmt.Errorf("package channel is full"))
		}
	}
}

func (s *Server) parsePackage(transport TransportInterface) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		select {
		case p := <-s.pc:
			go transport.Handle(ctx, p.conn, p.cmd, p.buff)

		case <-s.done:
			return
		}
	}
}

func (s *Server) Close() {
	_ = s.lis.Close()
	close(s.done)
}
