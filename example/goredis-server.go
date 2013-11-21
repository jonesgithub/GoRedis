// goredis-server启动函数
// @author latermoon

package main

import (
	"../goredis_server"
	"flag"
	"fmt"
	"os"
	"runtime"
)

// go run goredis-server.go -h localhost -p 1602
// go run goredis-server.go -procs 8 -p 17600
func main() {
	fmt.Println("GoRedis 0.1.1 by latermoon")

	hostPtr := flag.String("h", "", "Server host")
	portPtr := flag.Int("p", 1602, "Server port")
	procsPtr := flag.Int("procs", 8, "GOMAXPROCS")
	flag.Parse()

	runtime.GOMAXPROCS(*procsPtr)

	// db parent directory
	dbhome := "/data"
	finfo, e1 := os.Stat(dbhome)
	if os.IsNotExist(e1) || !finfo.IsDir() {
		dbhome = "/tmp"
	}

	directory := fmt.Sprintf("%s/goredis_%d/", dbhome, *portPtr)
	os.MkdirAll(directory, os.ModePerm)
	fmt.Println("dbhome:", directory)

	host := fmt.Sprintf("%s:%d", *hostPtr, *portPtr)

	// start ...
	server := goredis_server.NewGoRedisServer(directory)
	server.Init()
	server.Listen(host)
}
