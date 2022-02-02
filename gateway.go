package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"nhooyr.io/websocket"
)

// gatewayServer enables broadcasting to a set of subscribers.
type gatewayServer struct {
	// subscriberMessageBuffer controls the max number
	// of messages that can be queued for a subscriber
	// before it is kicked.
	//
	// Defaults to 16.
	subscriberMessageBuffer int

	// publishLimiter controls the rate limit applied to the publish endpoint.
	//
	// Defaults to one publish every 100ms with a burst of 8.
	publishLimiter *rate.Limiter

	// logf controls where logs are sent.
	// Defaults to log.Printf.
	logf func(f string, v ...interface{})

	// serveMux routes the various endpoints to the appropriate handler.
	serveMux http.ServeMux

	subscribersMu sync.RWMutex
	subscribers   map[string]*subscriber
}

// newGatewayServer constructs a gatewayServer with the defaults.
func newGatewayServer() *gatewayServer {
	gs := &gatewayServer{
		subscriberMessageBuffer: 16,
		logf:                    log.Printf,
		subscribers:             make(map[string]*subscriber),
		publishLimiter:          rate.NewLimiter(rate.Every(time.Millisecond*100), 8),
	}
	gs.serveMux.Handle("/", http.FileServer(http.Dir(".")))
	gs.serveMux.HandleFunc("/subscribe", gs.subscribeHandler)
	gs.serveMux.HandleFunc("/publish", gs.publishHandler)

	// admin handlers
	gs.serveMux.HandleFunc("/init", gs.initHandler)
	gs.serveMux.HandleFunc("/pub", gs.pubHandler)
	gs.serveMux.HandleFunc("/subs", gs.subsHandler)
	// public handlers
	gs.serveMux.HandleFunc("/sub", gs.subHandler)

	return gs
}

// subscriber represents a subscriber.
// Messages are sent on the msgs channel and if the client
// cannot keep up with the messages, closeSlow is called.
type subscriber struct {
	msgs      chan []byte
	closeSlow func()
}

func (gs *gatewayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	gs.serveMux.ServeHTTP(w, r)
}

// subscribeHandler accepts the WebSocket connection and then subscribes
// it to all future messages.
func (gs *gatewayServer) subscribeHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		gs.logf("%v", err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "")

	err = gs.subscribe(r.Context(), c)
	if errors.Is(err, context.Canceled) {
		return
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway {
		return
	}
	if err != nil {
		gs.logf("%v", err)
		return
	}
}

// publishHandler reads the request body with a limit of 8192 bytes and then publishes
// the received message.
func (gs *gatewayServer) publishHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	body := http.MaxBytesReader(w, r.Body, 8192)
	msg, err := ioutil.ReadAll(body)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}

	gs.publish(msg)

	w.WriteHeader(http.StatusAccepted)
}

// initHandler initializes a subscriber and returns its key
func (gs *gatewayServer) initHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	key, _ := gs.insertSubscriber()
	resp := &initResp{
		Key: key,
	}

	respBytes, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

func (gs *gatewayServer) pubHandler(w http.ResponseWriter, r *http.Request) {

}

func (gs *gatewayServer) subsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	keys, _ := gs.selectAllSubscribers()
	resp := &subsResp{
		Keys: keys,
	}

	respBytes, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

func (gs *gatewayServer) subHandler(w http.ResponseWriter, r *http.Request) {

}

// subscribe subscribes the given WebSocket to all broadcast messages.
// It creates a subscriber with a buffered msgs chan to give some room to slower
// connections and then registers the subscriber. It then listens for all messages
// and writes them to the WebSocket. If the context is cancelled or
// an error occurs, it returns and deletes the subscription.
//
// It uses CloseRead to keep reading from the connection to process control
// messages and cancel the context if the connection drops.
func (gs *gatewayServer) subscribe(ctx context.Context, c *websocket.Conn) error {
	ctx = c.CloseRead(ctx)

	s := &subscriber{
		msgs: make(chan []byte, gs.subscriberMessageBuffer),
		closeSlow: func() {
			c.Close(websocket.StatusPolicyViolation, "connection too slow to keep up with messages")
		},
	}
	key, err := GenerateRandomString(16)
	if err != nil {
		return err
	}
	gs.addSubscriber(key, s)
	defer gs.deleteSubscriber(key)

	for {
		select {
		case msg := <-s.msgs:
			err := writeTimeout(ctx, time.Second*5, c, msg)
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// publish publishes the msg to all subscribers.
// It never blocks and so messages to slow subscribers
// are dropped.
func (gs *gatewayServer) publish(msg []byte) {
	gs.subscribersMu.Lock()
	defer gs.subscribersMu.Unlock()

	gs.publishLimiter.Wait(context.Background())

	for _, s := range gs.subscribers {
		if s != nil {
			select {
			case s.msgs <- msg:
			default:
				go s.closeSlow()
			}
		}
	}
}

// addSubscriber registers a subscriber.
func (gs *gatewayServer) addSubscriber(key string, s *subscriber) {
	gs.logf("addSubscriber called, adding %v", key)
	gs.subscribersMu.Lock()
	gs.subscribers[key] = s
	gs.subscribersMu.Unlock()
}

// insertSubscriber initializes a subscriber with a random key
func (gs *gatewayServer) insertSubscriber() (string, error) {
	key, _ := GenerateRandomString(16)
	gs.logf("insertSubscriber called, inserting %v", key)
	gs.subscribersMu.Lock()
	gs.subscribers[key] = nil
	gs.subscribersMu.Unlock()
	return key, nil
}

// deleteSubscriber deletes the given subscriber.
func (gs *gatewayServer) deleteSubscriber(key string) {
	gs.logf("deleteSubscriber called, deleting %v", key)
	gs.subscribersMu.Lock()
	delete(gs.subscribers, key)
	gs.subscribersMu.Unlock()
}

// updateSubscriber updates the subscriber with a connection
func (gs *gatewayServer) updateSubscriber(key string, s *subscriber) error {
	s, err := gs.selectSubscriber(key)
	if err != nil {
		return err
	}
	if s != nil {
		return errors.New("subscriber has already been connected")
	}
	gs.subscribersMu.Lock()
	gs.subscribers[key] = s
	gs.subscribersMu.Unlock()
	return nil
}

// selectSubscriber selects the specified subscriber
func (gs *gatewayServer) selectSubscriber(key string) (*subscriber, error) {
	gs.subscribersMu.RLock()
	s, exists := gs.subscribers[key]
	gs.subscribersMu.RUnlock()
	if exists {
		return s, nil
	}
	return nil, errors.New("subscriber not found")
}

// selectAllSubscribers selects all subscribers
func (gs *gatewayServer) selectAllSubscribers() ([]string, error) {
	keys := make([]string, 0, len(gs.subscribers))
	for key := range gs.subscribers {
		keys = append(keys, key)
	}
	return keys, nil
}

func writeTimeout(ctx context.Context, timeout time.Duration, c *websocket.Conn, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return c.Write(ctx, websocket.MessageText, msg)
}
