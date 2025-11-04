package base

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/siddontang/go-log/log"
)

func ParserAllBinEventsFromRepl(cfg *ConfCmd) {
	defer cfg.CloseChan()
	cfg.BinlogStreamer = NewReplBinlogStreamer(cfg)
	log.Info("start to get binlog from mysql")
	SendBinlogEventRepl(cfg)
	log.Info("finish getting binlog from mysql")
}

func NewReplBinlogStreamer(cfg *ConfCmd) *replication.BinlogStreamer {
	replCfg := replication.BinlogSyncerConfig{
		ServerID:                uint32(cfg.ServerId),
		Flavor:                  cfg.MysqlType,
		Host:                    cfg.Host,
		Port:                    uint16(cfg.Port),
		User:                    cfg.User,
		Password:                cfg.Passwd,
		Charset:                 "utf8",
		SemiSyncEnabled:         false,
		TimestampStringLocation: GBinlogTimeLocation,
		ParseTime:               false, //donot parse mysql datetime/time column into go time structure, take it as string
		UseDecimal:              false, // sqlbuilder not support decimal type
	}

	replSyncer := replication.NewBinlogSyncer(replCfg)

	syncPosition := mysql.Position{Name: cfg.StartFile, Pos: uint32(cfg.StartPos)}
	replStreamer, err := replSyncer.StartSync(syncPosition)
	if err != nil {
		log.Fatalf(fmt.Sprintf("error replication from master %s:%d %v", cfg.Host, cfg.Port, err))
	}
	return replStreamer
}

func SendBinlogEventRepl(cfg *ConfCmd) {
	var (
		err           error
		ev            *replication.BinlogEvent
		chkRe         int
		currentBinlog string = cfg.StartFile
		binEventIdx   uint64 = 0
		trxIndex      uint64 = 0
		trxStatus     int    = 0
		sqlLower      string = ""

		db      string = ""
		tb      string = ""
		sql     string = ""
		sqlType string = ""
		rowCnt  uint32 = 0

		tbMapPos uint32 = 0

		//justStart   bool = true
		//orgSqlEvent *replication.RowsQueryEvent
		currentGtid string = "" // 新增：记录当前事务的 GTID
	)
	for {

		if cfg.OutputToScreen {
			ev, err = cfg.BinlogStreamer.GetEvent(context.Background())
			if err != nil {
				log.Fatalf("error to get binlog event")
				break
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), EventTimeout)
			ev, err = cfg.BinlogStreamer.GetEvent(ctx)
			cancel()
			if err == context.Canceled {
				log.Infof("ready to quit! [%v]", err)
				break
			} else if err == context.DeadlineExceeded {
				log.Infof("deadline exceeded.")
				break
			} else if err != nil {
				log.Fatalf(fmt.Sprintf("error to get binlog event %v", err))
				break
			}
		}

		// if ev.Header.EventType == replication.TABLE_MAP_EVENT {
		// 	tbMapPos = ev.Header.LogPos - ev.Header.EventSize
		// 	// avoid mysqlbing mask the row event as unknown table row event
		// }

		// 解析 GTID 事件，更新 currentGtid
		switch ev.Header.EventType {
		case replication.GTID_EVENT:
			// 类型断言为 GTIDEvent
			if gtidEvent, ok := ev.Event.(*replication.GTIDEvent); ok {
				sid := gtidEvent.SID // [16]byte
				// 手动格式化为 UUID 字符串
				uuidStr := fmt.Sprintf("%x-%x-%x-%x-%x",
					sid[0:4], sid[4:6], sid[6:8], sid[8:10], sid[10:16],
				)
				currentGtid = fmt.Sprintf("%s:%d", uuidStr, gtidEvent.GNO)
				// fmt.Println(currentGtid)
			}

		case replication.ANONYMOUS_GTID_EVENT:
			currentGtid = "ANONYMOUS"

		case replication.ROTATE_EVENT:
			// 切换 binlog 文件
			rotateEvent := ev.Event.(*replication.RotateEvent)
			currentBinlog = string(rotateEvent.NextLogName)

		case replication.TABLE_MAP_EVENT:
			tbMapPos = ev.Header.LogPos - ev.Header.EventSize
		}
		ev.RawData = []byte{} // we donnot need raw data

		oneMyEvent := &MyBinEvent{
			MyPos:       mysql.Position{Name: currentBinlog, Pos: ev.Header.LogPos},
			StartPos:    tbMapPos,
			IfRowsEvent: false,
			GtidStr:     currentGtid, // 赋值 GTID
		}
		chkRe = oneMyEvent.CheckBinEvent(cfg, ev, &currentBinlog)

		if chkRe == C_reContinue {
			continue
		} else if chkRe == C_reBreak {
			break
		} else if chkRe == C_reFileEnd {
			continue
		}

		db, tb, sqlType, sql, rowCnt = GetDbTbAndQueryAndRowCntFromBinevent(ev)
		//if find := strings.Contains(db, "#"); find {
		//	log.Fatalf(fmt.Sprintf("Unsupported database name %s contains special character '#'", db))
		//	break
		//}
		//if find := strings.Contains(tb, "#"); find {
		//	log.Fatalf(fmt.Sprintf("Unsupported table name %s.%s contains special character '#'", db, tb))
		//	break
		//}

		if sqlType == "query" {
			sqlLower = strings.ToLower(sql)
			if sqlLower == "begin" {
				trxStatus = C_trxBegin
				trxIndex++
			} else if sqlLower == "commit" {
				trxStatus = C_trxCommit
			} else if sqlLower == "rollback" {
				trxStatus = C_trxRollback
			} else if oneMyEvent.QuerySql != nil {
				trxStatus = C_trxProcess
				rowCnt = 1
			}

		} else {
			trxStatus = C_trxProcess
		}

		if cfg.WorkType != "stats" {
			ifSendEvent := false
			if oneMyEvent.IfRowsEvent {

				tbKey := GetAbsTableName(string(oneMyEvent.BinEvent.Table.Schema),
					string(oneMyEvent.BinEvent.Table.Table))
				_, err = G_TablesColumnsInfo.GetTableInfoJson(string(oneMyEvent.BinEvent.Table.Schema),
					string(oneMyEvent.BinEvent.Table.Table))
				if err != nil {
					log.Fatalf(fmt.Sprintf("no table struct found for %s, it maybe dropped, skip it. RowsEvent position:%s",
						tbKey, oneMyEvent.MyPos.String()))
				}
				ifSendEvent = true
			}
			if ifSendEvent {
				binEventIdx++
				oneMyEvent.EventIdx = binEventIdx
				oneMyEvent.SqlType = sqlType
				oneMyEvent.Timestamp = ev.Header.Timestamp
				oneMyEvent.TrxIndex = trxIndex
				oneMyEvent.TrxStatus = trxStatus
				cfg.EventChan <- *oneMyEvent
			}
		}

		//output analysis result whatever the WorkType is
		if sqlType != "" {
			if sqlType == "query" {
				cfg.StatChan <- BinEventStats{Timestamp: ev.Header.Timestamp, Binlog: currentBinlog, StartPos: ev.Header.LogPos - ev.Header.EventSize, StopPos: ev.Header.LogPos,
					Database: db, Table: tb, QuerySql: sql, RowCnt: rowCnt, QueryType: sqlType}
			} else {
				cfg.StatChan <- BinEventStats{Timestamp: ev.Header.Timestamp, Binlog: currentBinlog, StartPos: tbMapPos, StopPos: ev.Header.LogPos,
					Database: db, Table: tb, QuerySql: sql, RowCnt: rowCnt, QueryType: sqlType}
			}
		}

	}
}
