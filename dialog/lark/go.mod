module github.com/nuln/agent-plugins/dialog/lark

go 1.25.8

require github.com/nuln/agent-core v0.0.0

require github.com/larksuite/oapi-sdk-go/v3 v3.5.3

require (
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
)

replace github.com/nuln/agent-core => ../../../agent-core
