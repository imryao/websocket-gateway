package main

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
)

// publishHandler reads the request body with a limit of 8192 bytes and then publishes
// the received message.
func (gs *gatewayServer) broadcastHandler(w http.ResponseWriter, r *http.Request) {
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

	gs.broadcast(msg)

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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (gs *gatewayServer) pubHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	var req pubReq
	json.NewDecoder(r.Body).Decode(&req)

	gs.pub([]byte(req.Msg), req.Keys)

	w.WriteHeader(http.StatusAccepted)
}

func (gs *gatewayServer) subsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	keys, _ := gs.selectAllSubscriberKeys()
	resp := &subsResp{
		Keys: keys,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
