package render

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxMessage caps one DevTools message; the DOM snapshot, the largest, is
// cut in the page to fetch.MaxBytes characters, at most 4 bytes each.
const maxMessage = 12 << 20

// cdp speaks the DevTools protocol over the pipe --remote-debugging-pipe
// opens: JSON messages, each ended by a NUL byte. It opens no port, so no
// other process on the machine can drive the browser.
type cdp struct {
	w       io.Writer
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int64
	pending map[int64]chan message
	events  chan message
	done    chan struct{} // closed when the pipe from Chrome ends
	err     error         // why it ended, set before done closes
}

type message struct {
	ID        int64          `json:"id,omitzero"`
	Method    string         `json:"method,omitempty"`
	SessionID string         `json:"sessionId,omitempty"`
	Params    any            `json:"params,omitempty"`
	Result    jsontext.Value `json:"result,omitzero"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// incoming is a message from Chrome, its params kept raw.
type incoming struct {
	message
	Params jsontext.Value `json:"params,omitzero"`
}

// watched are the events a render waits for; the rest are dropped, so that
// a page's stream of others cannot crowd them out.
var watched = map[string]bool{"Page.loadEventFired": true, "Inspector.targetCrashed": true, "Target.targetCrashed": true}

func newCDP(w io.Writer, r io.Reader) *cdp {
	c := &cdp{w: w, pending: map[int64]chan message{}, events: make(chan message, 64), done: make(chan struct{})}
	go c.read(r)
	return c
}

func (c *cdp) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var err error
	for {
		var raw []byte
		if raw, err = readMessage(br); err != nil {
			break
		}
		var in incoming
		if err = json.Unmarshal(raw, &in); err != nil {
			break
		}
		in.message.Params = in.Params
		if in.ID == 0 {
			if watched[in.Method] {
				select {
				case c.events <- in.message:
				default: // nobody waits for this one
				}
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[in.ID]
		delete(c.pending, in.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- in.message
		}
	}
	if errors.Is(err, io.EOF) {
		err = errors.New("headless Chrome closed its DevTools pipe")
	}
	c.err = err
	close(c.done)
}

// readMessage reads up to the next NUL, refusing a message over maxMessage.
func readMessage(br *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for {
		chunk, err := br.ReadSlice(0)
		buf.Write(chunk)
		if buf.Len() > maxMessage {
			return nil, fmt.Errorf("a DevTools message over %d MB", maxMessage>>20)
		}
		switch {
		case err == nil:
			return buf.Bytes()[:buf.Len()-1], nil
		case !errors.Is(err, bufio.ErrBufferFull):
			return nil, err
		}
	}
}

// call sends one command (to a page when session is set) and decodes its
// result into out, if out is not nil.
func (c *cdp) call(ctx context.Context, session, method string, params, out any) error {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	raw, err := json.Marshal(message{ID: id, Method: method, SessionID: session, Params: params})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	_, err = c.w.Write(append(raw, 0))
	c.wmu.Unlock()
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		if out == nil || len(m.Result) == 0 {
			return nil
		}
		return json.Unmarshal(m.Result, out)
	case <-c.done:
		return fmt.Errorf("%s: %w", method, c.err)
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// waitEvent waits for an event named method on session, or ctx's end.
func (c *cdp) waitEvent(ctx context.Context, session, method string) (jsontext.Value, error) {
	for {
		select {
		case e := <-c.events:
			if e.Method == "Inspector.targetCrashed" || e.Method == "Target.targetCrashed" {
				return nil, errors.New("the page crashed headless Chrome")
			}
			if e.Method == method && e.SessionID == session {
				raw, _ := e.Params.(jsontext.Value)
				return raw, nil
			}
		case <-c.done:
			return nil, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
