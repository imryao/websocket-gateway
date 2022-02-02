package main

type initResp struct {
	Key string `json:"key"`
}

type subsResp struct {
	Keys []string `json:"keys"`
}
