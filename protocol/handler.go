package protocol

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server represents a server
type Server struct {
	c       *Connection
	opts    Opts
	storage *Storage
	// offset  int

	// for master only
	// slaves *Slaves
	// wg     *sync.WaitGroup
	mc *MasterConfig
}

// NewMaster is the master constructor
func NewMaster(conn *Connection, o Opts, mc *MasterConfig) *Server {
	return &Server{
		c:       conn,
		opts:    o,
		storage: storage,
		// slaves:  list,
		mc: mc,
	}
}

type MasterConfig struct {
	// storage    *Storage
	slaves     *Slaves
	wg         *sync.WaitGroup
	propOffset int
}

func NewMasterConfig() *MasterConfig {
	return &MasterConfig{
		// storage:    storage,
		slaves:     list,
		propOffset: 0,
	}
}

// NewSlave is the slave constructor
func NewSlave(conn *Connection) *Server {
	return &Server{
		c:       conn,
		storage: storage,
	}
}

// Handle reads and handles
func (s *Server) Handle() {
	defer s.c.Close()

	processRDB(s)

	for {
		o, request, err := s.Read()
		if err != nil {
			fmt.Printf("conn.Read() failed: %v\n", err)
			return
		}

		fmt.Printf("Received request: %v\n", request)

		err = s.HandleRequest(request)
		if err != nil {
			fmt.Printf("protocol.HandleRequest() failed: %v\n", err)
		}

		if s.opts.Role != "master" && (len(request) <= 1 || request[1] != "GETACK") {
			s.c.offset += o
		}
	}
}

var (
	waitLock = sync.Mutex{}
)

// HandleRequest responds to the request recieved.
func (s *Server) HandleRequest(request []string) error {
	if len(request) == 0 {
		return fmt.Errorf("empty request")
	}

	// var remoteAddr string

	switch strings.ToUpper(request[0]) {
	case "PING":
		err := handlePing(s)
		if err != nil {
			return fmt.Errorf("PING failed: %v", err)
		}
	case "ECHO":
		if len(request) != 2 {
			return fmt.Errorf("ECHO expects 1 argument")
		}
		err := handleEcho(s, request[1])
		if err != nil {
			return fmt.Errorf("ECHO failed: %v", err)
		}
	case "SET":
		err := handleSet(s, request[1:])
		if err != nil {
			return fmt.Errorf("SET failed: %v", err)
		}

		if s.opts.Role == "master" {
			err := handlePropagation(s, request)
			if err != nil {
				return fmt.Errorf("Propagation failed: %v", err)
			}
		}
	case "GET":
		if len(request) != 2 {
			return fmt.Errorf("GET expects 1 argument")
		}
		err := handleGet(s, request[1])
		if err != nil {
			return fmt.Errorf("GET failed: %v", err)
		}
	case "INFO":
		if len(request) != 2 {
			return fmt.Errorf("INFO expects 1 argument")
		}
		err := handleInfo(request[1], s)
		if err != nil {
			return fmt.Errorf("INFO failed: %v", err)
		}
	case "REPLCONF":
		err := handleReplconf(s, request[1:])
		if err != nil {
			return fmt.Errorf("REPLCONF failed: %v", err)
		}
	case "PSYNC":
		err := handlePsync(request[1:], s)
		if err != nil {
			return fmt.Errorf("PSYNC failed: %v", err)
		}

		s.mc.slaves.AddSlave(s.c.conn.RemoteAddr(), s.c)
	case "WAIT":
		waitLock.Lock()
		err := handleWait(request[1:], s)
		waitLock.Unlock()
		if err != nil {
			return fmt.Errorf("WAIT failed: %v", err)
		}
	case "CONFIG":
		if request[1] == "GET" {
			err := handleConfigGet(request[2:], s)
			if err != nil {
				return fmt.Errorf("CONFIG GET failed: %v", err)
			}
		} else {
			return fmt.Errorf("Invalid CONFIG command: %s", request[1])
		}
	case "KEYS":
		if request[1] == "*" {
			err := handleKeys(s)
			if err != nil {
				return fmt.Errorf("KEYS failed: %v", err)
			}
		}
	case "TYPE":
		err := handleType(request[1:], s)
		if err != nil {
			return fmt.Errorf("TYPE failed: %v", err)
		}
	default:
		return fmt.Errorf("unknown command: %s", request[0])
	}

	return nil
}

func handleEcho(s *Server, message string) error {
	err := s.c.Write(fmt.Sprintf("$%d\r\n%s\r\n", len(message), message))
	if err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}

	return nil
}

func handlePing(s *Server) error {
	if s.opts.Role == "master" {
		err := s.c.Write("+PONG\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
	}

	return nil
}

func handleSet(s *Server, request []string) error {
	key := request[0]
	value := request[1]

	var expireAt int64
	if len(request) == 4 {
		expireAfter, err := strconv.ParseInt(request[3], 10, 64)
		if err != nil {
			return fmt.Errorf("Atoi failed: %v", err)
		}
		expireAt = time.Now().UnixMilli() + expireAfter
	}

	s.storage.cache[key] = NewEntry(value, expireAt)

	if s.opts.Role == "master" {
		err := s.c.Write("+OK\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
	}

	return nil
}

func handleGet(s *Server, key string) error {
	now := time.Now().UnixMilli()

	entry, ok := s.storage.cache[key]
	if !ok {
		err := s.c.Write("$-1\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
		return nil
	}

	if entry.expireAt != 0 && now > entry.expireAt {
		err := s.c.Write("$-1\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
		delete(s.storage.cache, key)
		return nil
	}

	err := s.c.Write(fmt.Sprintf("$%d\r\n%s\r\n", len(entry.msg), entry.msg))
	if err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}

	return nil
}

func handleInfo(arg string, server *Server) error {
	var s string

	if arg == "replication" {
		s += "# Replication\r\n"
		if server.opts.Role == "slave" {
			s += "role:slave\r\n"
		} else {
			s += "role:master\r\n"
		}

		s += fmt.Sprintf("master_replid:%s\r\n", server.opts.ReplID)

		s += fmt.Sprintf("master_repl_offset:%d\r\n", server.mc.propOffset)
	}

	err := server.c.Write(fmt.Sprintf("$%d\r\n%s\r\n", len(s), s))
	if err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}
	return nil
}

func handleReplconf(server *Server, request []string) error {
	switch request[0] {
	case "ACK":
		// This logic is ran by master
		ack, err := strconv.Atoi(request[1])
		if err != nil {
			return fmt.Errorf("strconf.Atoi failed: %v", err)
		}

		if err := server.mc.slaves.Ack(server.c.conn.RemoteAddr(), ack); err != nil {
			return fmt.Errorf("ack slave response filed: %w", err)
		}

		if server.mc.wg != nil {
			server.mc.wg.Done()
		}
	case "GETACK":
		// This logic is ran by slave
		err := server.c.Write(fmt.Sprintf("*3\r\n$8\r\nREPLCONF\r\n$3\r\nACK\r\n$%d\r\n%d\r\n", len(strconv.Itoa(server.c.offset)), server.c.offset))
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}

		fmt.Println("asdfadsfafafs")

		server.c.offset += 37
	default:
		err := server.c.Write("+OK\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
	}

	return nil
}

func handlePsync(request []string, server *Server) error {
	emptyRDB, err1 := base64.StdEncoding.DecodeString("UkVESVMwMDEx+glyZWRpcy12ZXIFNy4yLjD6CnJlZGlzLWJpdHPAQPoFY3RpbWXCbQi8ZfoIdXNlZC1tZW3CsMQQAPoIYW9mLWJhc2XAAP/wbjv+wP9aog==")
	if err1 != nil {
		return fmt.Errorf("DecodeString failed: %v", err1)
	}

	if request[0] == "?" {
		err2 := server.c.Write(fmt.Sprintf("+FULLRESYNC %s 0\r\n", server.opts.ReplID))
		if err2 != nil {
			return fmt.Errorf("Write failed: %v", err2)
		}
	}

	err := server.c.Write(fmt.Sprintf("$%d\r\n%s", len(string(emptyRDB)), string(emptyRDB)))
	if err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}

	server.mc.slaves.AddSlave(server.c.conn.RemoteAddr(), server.c)

	return nil
}

func handlePropagation(master *Server, request []string) error {
	propCmd := ToRespArray(request)
	if err := master.mc.slaves.Propagate(propCmd); err != nil {
		return fmt.Errorf("cannot propagate: %w", err)
	}
	master.mc.propOffset += len(propCmd)

	return nil
}

func handleWait(request []string, master *Server) error {
	numReplicas, err := strconv.Atoi(request[0])
	if err != nil {
		return fmt.Errorf("Atoi failed: %v", err)
	}

	t, err := strconv.Atoi(request[1])
	if err != nil {
		return fmt.Errorf("Atoi failed: %v", err)
	}

	acked := master.mc.slaves.SyncedSlaveCount(master.mc.propOffset)

	if acked >= numReplicas {
		err = master.c.Write(fmt.Sprintf(":%d\r\n", acked))
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}

		return nil
	}

	if err := master.mc.slaves.Propagate("*3\r\n$8\r\nREPLCONF\r\n$6\r\nGETACK\r\n$1\r\n*\r\n"); err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}

	master.mc.wg = &sync.WaitGroup{}
	master.mc.wg.Add(min(numReplicas, master.mc.slaves.Count()))

	ch := make(chan struct{})
	go func() {
		defer close(ch)
		master.mc.wg.Wait()
	}()

	select {
	case <-ch:
	case <-time.After(time.Duration(t) * time.Millisecond):
	}

	acked = master.mc.slaves.SyncedSlaveCount(master.mc.propOffset)

	fmt.Printf("master.offset = %d, acked = %d\n", master.mc.propOffset, acked)
	err = master.c.Write(fmt.Sprintf(":%d\r\n", acked))
	if err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}

	master.mc.propOffset += 37

	return nil
}

func handleConfigGet(request []string, s *Server) error {
	switch request[0] {
	case "dir":
		s.c.Write(fmt.Sprintf("*2\r\n$3\r\ndir\r\n$%d\r\n%s\r\n", len(s.opts.Dir), s.opts.Dir))
	case "dbfilename":
		s.c.Write(fmt.Sprintf("*2\r\n$3\r\ndbfilename\r\n$%d\r\n%s\r\n", len(s.opts.Dbfilename), s.opts.Dbfilename))
	default:
		return fmt.Errorf("Invalid config get param: %v", request[0])
	}

	return nil
}

func handleKeys(s *Server) error {
	var keys []string
	for k := range s.storage.cache {
		fmt.Printf("Found key: %s\n", k)
		keys = append(keys, k)
	}

	err := s.c.Write(ToRespArray(keys))
	if err != nil {
		return fmt.Errorf("Write failed: %v", err)
	}

	return nil
}

func handleType(request []string, s *Server) error {
	if len(request) > 1 {
		return fmt.Errorf("Invalid key in type command: %s", request)
	}

	_, ok := s.storage.cache[request[0]]
	if ok {
		err := s.c.Write("+string\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
	} else {
		err := s.c.Write("+none\r\n")
		if err != nil {
			return fmt.Errorf("Write failed: %v", err)
		}
	}

	return nil
}
