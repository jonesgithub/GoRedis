package goredis_server

import (
	. "../goredis"
	"./libs/rdb"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	SSConnecting     = "connecting"
	SSDisconnected   = "disconnected"
	SSRdbRecving     = "rdbrecving"
	SSCommandRecving = "commandrecving"
)

type SlaveSession struct {
	session        *Session
	status         string
	masterHost     string
	DidRecvCommand func(cmd *Command, count int64, isrdb bool)
	RdbFinished    func(count int64)
	didRecvCommand func(cmd *Command, isrdb bool)
	totalCount     int64
}

func NewSlaveSession(sess *Session, masterHost string) (s *SlaveSession) {
	s = &SlaveSession{}
	s.session = sess
	s.status = SSDisconnected
	s.masterHost = masterHost
	s.totalCount = 0
	s.didRecvCommand = func(cmd *Command, isrdb bool) {
		s.totalCount++
		s.DidRecvCommand(cmd, s.totalCount, isrdb)
	}
	s.RdbFinished = func(count int64) {}
	return
}

func (s *SlaveSession) Session() (sess *Session) {
	return s.session
}

func (s *SlaveSession) MasterHost() string {
	return s.masterHost
}

func (s *SlaveSession) RemoteAddr() net.Addr {
	return s.session.RemoteAddr()
}

func (s *SlaveSession) Sync(uid string) (err error) {
	s.status = SSConnecting
	var isgoredis bool
	isgoredis, err = s.isSlaveOfGoRedis()
	if err != nil {
		return
	}
	if isgoredis {
		s.session.WriteCommand(NewCommand([]byte("SYNC"), []byte("UID"), []byte(uid)))
	} else {
		s.session.WriteCommand(NewCommand([]byte("SYNC")))
	}
	readRdbFinish := false
	var c byte
	for {
		c, err = s.session.PeekByte()
		if err != nil {
			fmt.Printf("master gone away %s\n", s.session.RemoteAddr())
			break
		}
		if c == '*' {
			if cmd, e2 := s.session.ReadCommand(); e2 == nil {
				// PUSH
				s.didRecvCommand(cmd, false)
			} else {
				fmt.Printf("sync error %s\n", e2)
				err = e2
				break
			}
		} else if !readRdbFinish && c == '$' {
			fmt.Printf("[%s] sync rdb \n", s.session.RemoteAddr())
			s.session.ReadByte()
			var count int64
			count, err = s.session.ReadLineInteger()
			if err != nil {
				break
			}
			rdbsize := int(count)

			fmt.Printf("[%s] rdb size %d bytes\n", s.session.RemoteAddr(), rdbsize)
			// read
			dec := newDecoder(s)
			err = rdb.Decode(s.session, dec)
			if err != nil {
				break
			}
			readRdbFinish = true
		} else {
			fmt.Printf("[%s] skip byte %q %s\n", s.session.RemoteAddr(), c, string(c))
			_, err = s.session.ReadByte()
			if err != nil {
				break
			}
		}
	}
	return
}

func (s *SlaveSession) isSlaveOfGoRedis() (isGoRedis bool, err error) {
	// 检查是goredis还是官方redis
	var info string
	info, err = s.getRedisInfo("server")
	if err != nil {
		return
	}
	isGoRedis = strings.Index(info, "goredis_version") > 0
	return
}

// 获取redis info
func (s *SlaveSession) getRedisInfo(section string) (info string, err error) {
	cmdinfo := NewCommand([]byte("INFO"), []byte(section))
	s.session.WriteCommand(cmdinfo)
	var reply *Reply
	reply, err = s.session.ReadReply()
	if err == nil {
		switch reply.Value.(type) {
		case string:
			info = reply.Value.(string)
		case []byte:
			info = string(reply.Value.([]byte))
		default:
			info = reply.String()
		}
	}
	return
}

func (s *SlaveSession) Status() string {
	return s.status
}

func (s *SlaveSession) Disconnect() (err error) {
	s.status = SSDisconnected
	return
}

// =============================================
// 第三方rdb解释函数
// =============================================
type rdbDecoder struct {
	rdb.NopDecoder
	db       int
	i        int
	keyCount int64
	bufsize  int
	session  *SlaveSession
	// 数据缓冲
	hashEntry [][]byte
	setEntry  [][]byte
	listEntry [][]byte
	zsetEntry [][]byte
}

func newDecoder(s *SlaveSession) (dec *rdbDecoder) {
	dec = &rdbDecoder{}
	dec.session = s
	dec.keyCount = 0
	dec.bufsize = 200
	return
}

func (p *rdbDecoder) StartDatabase(n int) {
	p.db = n
}

func (p *rdbDecoder) EndDatabase(n int) {
}

func (p *rdbDecoder) EndRDB() {
	p.session.RdbFinished(p.keyCount)
}

// Set
func (p *rdbDecoder) Set(key, value []byte, expiry int64) {
	cmd := NewCommand([]byte("SET"), key, value)
	p.session.didRecvCommand(cmd, true)
	p.keyCount++
}

func (p *rdbDecoder) StartHash(key []byte, length, expiry int64) {
	if int(length) < p.bufsize {
		p.hashEntry = make([][]byte, 0, length+2)
	} else {
		p.hashEntry = make([][]byte, 0, p.bufsize)
	}
	p.hashEntry = append(p.hashEntry, []byte("HSET"))
	p.hashEntry = append(p.hashEntry, key)
	p.keyCount++
}

func (p *rdbDecoder) Hset(key, field, value []byte) {
	p.hashEntry = append(p.hashEntry, field)
	p.hashEntry = append(p.hashEntry, value)
	if len(p.hashEntry) >= p.bufsize {
		cmd := NewCommand(p.hashEntry...)
		p.session.didRecvCommand(cmd, true)
		p.hashEntry = make([][]byte, 0, p.bufsize)
		p.hashEntry = append(p.hashEntry, []byte("HSET"))
		p.hashEntry = append(p.hashEntry, key)
	}
}

// Hash
func (p *rdbDecoder) EndHash(key []byte) {
	if len(p.hashEntry) > 2 {
		cmd := NewCommand(p.hashEntry...)
		p.session.didRecvCommand(cmd, true)
	}
}

func (p *rdbDecoder) StartSet(key []byte, cardinality, expiry int64) {
	if int(cardinality) < p.bufsize {
		p.setEntry = make([][]byte, 0, cardinality+2)
	} else {
		p.setEntry = make([][]byte, 0, p.bufsize)
	}
	p.setEntry = append(p.setEntry, []byte("SADD"))
	p.setEntry = append(p.setEntry, key)
	p.keyCount++
}

func (p *rdbDecoder) Sadd(key, member []byte) {
	p.setEntry = append(p.setEntry)
	if len(p.setEntry) >= p.bufsize {
		cmd := NewCommand(p.setEntry...)
		p.session.didRecvCommand(cmd, true)
		p.setEntry = make([][]byte, 0, p.bufsize)
		p.setEntry = append(p.setEntry, []byte("SADD"))
		p.setEntry = append(p.setEntry, key)
	}
}

// Set
func (p *rdbDecoder) EndSet(key []byte) {
	if len(p.setEntry) > 2 {
		cmd := NewCommand(p.setEntry...)
		p.session.didRecvCommand(cmd, true)
	}
}

func (p *rdbDecoder) StartList(key []byte, length, expiry int64) {
	if int(length) < p.bufsize {
		p.listEntry = make([][]byte, 0, length+2)
	} else {
		p.listEntry = make([][]byte, 0, p.bufsize)
	}
	p.listEntry = append(p.listEntry, []byte("RPUSH"))
	p.listEntry = append(p.listEntry, key)
	p.keyCount++
	p.i = 0
}

func (p *rdbDecoder) Rpush(key, value []byte) {
	p.listEntry = append(p.listEntry, value)
	if len(p.listEntry) >= p.bufsize {
		cmd := NewCommand(p.listEntry...)
		p.session.didRecvCommand(cmd, true)
		p.listEntry = make([][]byte, 0, p.bufsize)
		p.listEntry = append(p.listEntry, []byte("RPUSH"))
		p.listEntry = append(p.listEntry, key)
	}
	p.i++
}

// List
func (p *rdbDecoder) EndList(key []byte) {
	if len(p.listEntry) > 2 {
		cmd := NewCommand(p.listEntry...)
		p.session.didRecvCommand(cmd, true)
	}
}

func (p *rdbDecoder) StartZSet(key []byte, cardinality, expiry int64) {
	if int(cardinality) > p.bufsize {
		p.zsetEntry = make([][]byte, 0, cardinality)
	} else {
		p.zsetEntry = make([][]byte, 0, p.bufsize)
	}
	p.zsetEntry = append(p.zsetEntry, []byte("ZADD"))
	p.zsetEntry = append(p.zsetEntry, key)
	p.keyCount++
	p.i = 0
}

func (p *rdbDecoder) Zadd(key []byte, score float64, member []byte) {
	p.zsetEntry = append(p.zsetEntry, []byte(strconv.FormatInt(int64(score), 10)))
	p.zsetEntry = append(p.zsetEntry, member)
	if len(p.zsetEntry) >= p.bufsize {
		cmd := NewCommand(p.zsetEntry...)
		p.session.didRecvCommand(cmd, true)
		p.zsetEntry = make([][]byte, 0, p.bufsize)
		p.zsetEntry = append(p.zsetEntry, []byte("ZADD"))
		p.zsetEntry = append(p.zsetEntry, key)
	}
	p.i++
}

// ZSet
func (p *rdbDecoder) EndZSet(key []byte) {
	if len(p.zsetEntry) > 2 {
		cmd := NewCommand(p.zsetEntry...)
		p.session.didRecvCommand(cmd, true)
	}
}