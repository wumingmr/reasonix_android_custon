package main

type trashedSessionMeta struct {
	Key       string `json:"key"`
	DeletedAt int64  `json:"deletedAt"`
	Kind      string `json:"kind,omitempty"`
}
