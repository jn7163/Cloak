package multiplex

import (
	"errors"
	"log"
	"sync"
)

const (
	readBuffer = 20480
)

type Stream struct {
	id uint32

	session *Session

	// Copied from smux
	dieM sync.Mutex
	die  chan struct{}

	// Explanations of the following 4 fields can be found in frameSorter.go
	nextRecvSeq uint32
	rev         int
	sh          sorterHeap
	wrapMode    bool

	newFrameCh  chan *Frame
	sortedBufCh chan []byte

	nextSendSeqM sync.Mutex
	nextSendSeq  uint32

	closingM sync.Mutex
	closing  bool
}

func makeStream(id uint32, sesh *Session) *Stream {
	stream := &Stream{
		id:          id,
		session:     sesh,
		die:         make(chan struct{}),
		sh:          []*frameNode{},
		newFrameCh:  make(chan *Frame, 1024),
		sortedBufCh: make(chan []byte, 4096),
	}
	go stream.recvNewFrame()
	return stream
}

func (stream *Stream) Read(buf []byte) (n int, err error) {
	if len(buf) == 0 {
		select {
		case <-stream.die:
			log.Printf("Stream %v dying\n", stream.id)
			return 0, errors.New(errBrokenPipe)
		default:
			return 0, nil
		}
	}
	select {
	case <-stream.die:
		log.Printf("Stream %v dying\n", stream.id)
		return 0, errors.New(errBrokenPipe)
	case data := <-stream.sortedBufCh:
		if len(buf) < len(data) {
			log.Println(len(data))
			return 0, errors.New("buf too small")
		}
		copy(buf, data)
		return len(data), nil
	}

}

func (stream *Stream) Write(in []byte) (n int, err error) {
	select {
	case <-stream.die:
		log.Printf("Stream %v dying\n", stream.id)
		return 0, errors.New(errBrokenPipe)
	default:
	}

	var closingID uint32

	select {
	case closingID = <-stream.session.closeQCh:
	default:
	}

	f := &Frame{
		StreamID:        stream.id,
		Seq:             stream.nextSendSeq,
		ClosingStreamID: closingID,
		Payload:         in,
	}

	stream.nextSendSeqM.Lock()
	stream.nextSendSeq += 1
	stream.nextSendSeqM.Unlock()

	tlsRecord := stream.session.obfs(f)
	stream.session.sb.dispatCh <- tlsRecord

	return len(in), nil

}

func (stream *Stream) Close() error {
	log.Printf("ID: %v closing\n", stream.id)

	// Because closing a closed channel causes panic
	stream.closingM.Lock()
	defer stream.closingM.Unlock()
	if stream.closing {
		return errors.New(errRepeatStreamClosing)
	}
	stream.closing = true
	close(stream.die)
	stream.session.delStream(stream.id)
	stream.session.closeQCh <- stream.id
	return nil
}