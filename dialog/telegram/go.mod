module github.com/nuln/agent-plugins/dialog/telegram

go 1.25.8

require github.com/nuln/agent-core v0.0.0

require github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1

require github.com/robfig/cron/v3 v3.0.1 // indirect

replace github.com/nuln/agent-core => ../../../agent-core
