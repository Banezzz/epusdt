package dao

import (
	"github.com/assimon/luuu/config"
	"github.com/assimon/luuu/model/mdb"
	"github.com/assimon/luuu/util/log"
	"github.com/gookit/color"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var RuntimeDB *gorm.DB

func RuntimeInit() error {
	var err error
	runtimePath := config.GetRuntimeSqlitePath()
	color.Green.Printf("[runtime_db] sqlite filename: %s\n", runtimePath)
	RuntimeDB, err = openDB(runtimePath, &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
		Logger: logger.Default.LogMode(logger.Error),
	})
	if err != nil {
		color.Red.Printf("[runtime_db] sqlite open DB,err=%s\n", err)
		return err
	}

	sqlDB, err := RuntimeDB.DB()
	if err != nil {
		color.Red.Printf("[runtime_db] sqlite get DB,err=%s\n", err)
		return err
	}

	concurrency := config.GetQueueConcurrency()
	if concurrency < 2 {
		concurrency = 2
	}
	if concurrency > 16 {
		concurrency = 16
	}
	sqlDB.SetMaxOpenConns(concurrency)
	sqlDB.SetMaxIdleConns(1)

	if err = RuntimeDB.Exec("PRAGMA journal_mode=WAL;").Error; err != nil {
		return err
	}
	if err = RuntimeDB.Exec("PRAGMA synchronous=NORMAL;").Error; err != nil {
		return err
	}
	if err = RuntimeDB.Exec("PRAGMA busy_timeout=5000;").Error; err != nil {
		return err
	}
	if err = sqlDB.Ping(); err != nil {
		color.Red.Printf("[runtime_db] sqlite connDB err:%s", err.Error())
		return err
	}
	if err = RuntimeDB.AutoMigrate(&mdb.TransactionLock{}); err != nil {
		color.Red.Printf("[runtime_db] sqlite migrate DB(TransactionLock),err=%s\n", err)
		return err
	}

	log.Sugar.Debug("[runtime_db] sqlite connDB success")
	return nil
}
