package main

type initResp struct {
	Key string `json:"key"`
}

type subsResp struct {
	Keys []string `json:"keys"`
}

type pubReq struct {
	Keys []string `json:"keys"`
	Msg  string   `json:"msg"`
}
