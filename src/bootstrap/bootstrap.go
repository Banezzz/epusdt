package bootstrap

import (
	"github.com/assimon/luuu/command"
	"github.com/assimon/luuu/config"
	"github.com/assimon/luuu/model/dao"
	"github.com/assimon/luuu/mq"
	"github.com/assimon/luuu/task"
	"github.com/assimon/luuu/telegram"
	"github.com/assimon/luuu/util/log"
)

func Start() {
	config.Init()
	log.Init()
	dao.Init()
	mq.Start()
	go telegram.BotStart()
	go task.Start()

	if err := command.Execute(); err != nil {
		panic(err)
	}
}
