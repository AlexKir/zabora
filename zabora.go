package main

import (
	"fmt"
	"net"
	"os"
	"path"
	"zabora/pass"

	//"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io/ioutil"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antchfx/xmlquery"
	"github.com/coreos/go-systemd/daemon"

	_ "github.com/mattn/go-oci8"

	log "github.com/sirupsen/logrus"
	gcfg "gopkg.in/gcfg.v1"
	//my
	//"./pass"
)

// zabora Config
type myConfig struct {
	// Agent configuration
	Agent struct {
		Port             string
		ZabbixServer     string
		LogLevel         string
		LogFile          string
		SQLFile          []string
		UpdateSQLFileURL string
		DefDBUserName    string
		DefDBPassword    string
		DefMaxIdleConns  int
		DefMaxOpenConns  int
		DefIdleTimeOut   string
		DefSQLTimeOut    int
	}
	// Database connects configuration
	DBCFG map[string]*struct {
		ConnectSttring string
		UserName       string
		Password       string
		MaxIdleConns   int
		MaxOpenConns   int
	}
}

var (
	dbConn     map[string]*sql.DB
	cfg        myConfig
	itemSQL    map[string]string
	errCount   myCounter
	reqCount   myCounter
	netTimeOut = "60s"
	sqlTimeOut = 60
)

type myCounter struct {
	mu sync.Mutex
	x  int
}

func (c *myCounter) Inc() {
	c.mu.Lock()
	c.x++
	c.mu.Unlock()
}

func (c *myCounter) Value() (x int) {
	c.mu.Lock()
	x = c.x
	c.mu.Unlock()
	return
}

const (
	myVERSION   = "0.06"
	tcpProtocol = "tcp4"
	tcpAdress   = "0.0.0.0"
	//key         = "mykeyforpass1234"
	// https://www.zabbix.com/documentation/1.8/ru/protocols
	zabHeader       = "ZBXD\x01"
	zabNotSupported = "ZBX_NOTSUPPORTED\x00"
)

func main() {
	var err error
	var host string
	var lvl log.Level
	// Log configuration
	//log.SetOutput(os.Stderr)
	formatter := &log.TextFormatter{
		FullTimestamp: true,
	}
	log.SetFormatter(formatter)
	log.SetLevel(log.DebugLevel)

	// Parse config
	configFile := "zabora.cfg"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}
	err = gcfg.ReadFileInto(&cfg, configFile)
	if err != nil {
		log.Fatal("Error read config file: "+configFile+" ", err.Error())
	}
	if cfg.Agent.LogFile != "" {
		f, err := os.OpenFile(cfg.Agent.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
		if err != nil {
			log.Fatal("Can not open logfile ", cfg.Agent.LogFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
	}
	lvl, err = log.ParseLevel(cfg.Agent.LogLevel)
	if err != nil {
		log.SetLevel(log.InfoLevel)
		log.Error("Error parsing LogLevel ", cfg.Agent.LogLevel)
	} else {
		log.SetLevel(lvl)
	}
	keyfilepath := path.Dir(configFile) + "/.zabora.key"
	key, err := ioutil.ReadFile(keyfilepath)
	if err != nil {
		log.Error("Error read file " + keyfilepath + " : " + err.Error())
	}
	key = key[:len(key)-1] // remove end of line
	log.Debug("key : "+string(key)+", key len: ", len(key))

	log.Info("Zabora agent started pid ", os.Getpid())
	defer log.Info("Zabora agent shutdown", os.Getpid())
	//log.Debug("cfg ", cfg)
	//log.Debug("dbcfg ", cfg.DBCFG)
	// Parse SQL for items
	loadSQLFile(cfg.Agent.SQLFile)

	if cfg.Agent.DefSQLTimeOut != 0 {
		sqlTimeOut = cfg.Agent.DefSQLTimeOut
	}
	dbConn = make(map[string]*sql.DB)
	for d := range cfg.DBCFG {
		dbuser := cfg.Agent.DefDBUserName
		if cfg.DBCFG[d].UserName != "" {
			dbuser = cfg.DBCFG[d].UserName
		}
		dbpass, err := pass.DecPass(cfg.Agent.DefDBPassword, key)
		if err != nil {
			log.Fatal("Can not decrypt DB password ", err)
		}
		if cfg.DBCFG[d].Password != "" {
			dbpass, err = pass.DecPass(cfg.DBCFG[d].Password, key)
			if err != nil {
				log.Fatal("Can not decrypt DB "+d+" password ", err)
			}
		}

		connectString := dbuser + "/" + dbpass + "@" + cfg.DBCFG[d].ConnectSttring
		db, err := sql.Open("oci8", connectString)
		if err != nil {
			log.Error("Can not connect to DB ", d, err.Error())
			continue
			//			delete(dbConn, itemDB)
		}
		log.Info("Connect to DB ", d+" ", cfg.DBCFG[d].ConnectSttring)
		dbConn[d] = db
		dbIdleConns := cfg.Agent.DefMaxIdleConns
		if cfg.DBCFG[d].MaxIdleConns != 0 {
			dbIdleConns = cfg.DBCFG[d].MaxIdleConns
		}
		dbConn[d].SetMaxIdleConns(dbIdleConns)

		dbOpenConns := cfg.Agent.DefMaxOpenConns
		if cfg.DBCFG[d].MaxOpenConns != 0 {
			dbOpenConns = cfg.DBCFG[d].MaxOpenConns
		}
		dbConn[d].SetMaxOpenConns(dbOpenConns)
	}

	if len(dbConn) == 0 {
		log.Fatal("DB connecion not configured")
	}

	tcpPort := cfg.Agent.Port
	if cfg.Agent.DefIdleTimeOut != "" {
		netTimeOut = cfg.Agent.DefIdleTimeOut
	}
	idleTimeOut, _ := time.ParseDuration(netTimeOut)
	// Listen for incoming connections.
	l, err := net.Listen(tcpProtocol, ":"+tcpPort)
	if err != nil {
		log.Fatal("Error listening:", err.Error())
	}
	// Close the listener when the application closes.
	defer l.Close()
	log.Info("Listening on " + tcpAdress + ":" + tcpPort)
	// systemd service Type=notify
	if ok, err := daemon.SdNotify(false, "READY=1"); !ok || err != nil {
		if !ok && err == nil {
			log.Error("systemd notification not supported")
		} else {
			log.Error("systemd notification supported, but failure happened ", err.Error())
		}
	}
	// setup signal catching
	sigs := make(chan os.Signal, 1)
	// catch all signals since not explicitly listing
	signal.Notify(sigs, os.Interrupt)
	// method invoked upon seeing signal
	go func() {
		s := <-sigs
		daemon.SdNotify(false, "STOPPING=1")
		log.Info("RECEIVED SIGNAL: ", s)
		log.Info("Zabora agent shutdown ", os.Getpid())
		os.Exit(1)
	}()
	for {
		// Listen for an incoming connection.
		conn, err := l.Accept()
		if err != nil {
			log.Fatal("Error accepting: ", err.Error())
		}
		// Проверка входящего адреса
		// log.Debug("request from address ", conn.RemoteAddr().String())
		// func SplitHostPort(hostport string) (host, port string, err error)
		host, _, err = net.SplitHostPort(conn.RemoteAddr().String())
		if host != cfg.Agent.ZabbixServer {
			log.Error("Block connection from ", host, " accept connection only from ", cfg.Agent.ZabbixServer)
			conn.Close()

		} else {
			reqCount.Inc()
			conn.SetDeadline(time.Now().Add(idleTimeOut))
			// Handle connections in a new goroutine.
			go handleRequest(conn)
		}
	}
}

// Handles incoming requests.
func handleRequest(conn net.Conn) {
	// Close the connection when you're done with it.
	defer conn.Close()
	item := ""
	// Make a buffer to hold incoming data.
	// 4096 need for discovery
	buf := make([]byte, 4096)
	// Read the incoming connection into the buffer.
	reqLen, err := conn.Read(buf)
	if err != nil {
		log.Error("Error reading:", err.Error(), reqLen)
	} else {
		// zabbix 4.0
		// https://www.zabbix.com/documentation/4.0/manual/appendix/protocols/header_datalen
		// <PROTOCOL> - "ZBXD" (4 bytes).
		// <FLAGS> -the protocol flags, (1 byte). 0x01 - Zabbix communications protocol, 0x02 - compression).
		// <DATALEN> - data length (4 bytes). 1 will be formatted as 01/00/00/00 (four bytes, 32 bit number in little-endian format).
		// <RESERVED> - reserved for protocol extensions (4 bytes).
		// 4+1+4+4 -> 13
		//log.Debug("Received buffer:", buf)
		if string(buf[:5]) == "ZBXD\x01" {
			item = string(buf[13:reqLen])
			item = strings.TrimSuffix(item, "\n")
		} else {
			item = string(buf[:reqLen])
			item = strings.TrimSuffix(item, "\n")
			//item = strings.Trim(item, " ")
		}
	}
	value := ""
	log.Debug("Received request item: lenght = ", len(item), " item = ", item)
	switch {
	case item == "zabora.ping":
		value = "1"
	case item == "zabora.version":
		value = myVERSION
	case item == "zabora.reqcount":
		value = strconv.Itoa(reqCount.Value())
	case item == "zabora.errcount":
		value = strconv.Itoa(errCount.Value())
	case strings.HasPrefix(item, "oracle["):
		value, err = getDBValue(item)
		if err != nil {
			log.Error(item, value, err.Error())
			errCount.Inc()
		}
	default:
		value = zabNotSupported + " Unsupported item key. " + item
		log.Error(item, value)
		errCount.Inc()
	}
	// Send a response back to person contacting us.
	log.Debug("value = ", value, " item = ", item)
	zabDataLen := make([]byte, 8)
	binary.LittleEndian.PutUint32(zabDataLen, uint32(len(value)))

	log.Debug("value len = ", len(value), " z_adatalen = ", zabDataLen, " value len (hex) = ", fmt.Sprintf("%x", len(value)))
	_, err = conn.Write([]byte(zabHeader))
	if err != nil {
		log.Error("Net send error for "+item, err.Error())
		errCount.Inc()
		conn.Close()
		return
	}
	bufValue := append(zabDataLen, value...)
	log.Debug("value = ", bufValue)
	//_, err = conn.Write([]byte(value))
	_, err = conn.Write(bufValue)
	if err != nil {
		log.Error("Net send error for "+item, err.Error())
		errCount.Inc()
		conn.Close()
		return
	}
}

// Выполняем SQL
func getDBValue(item string) (string, error) {
	var (
		err       error
		itemKey   string
		itemDB    string
		itemParam string
		//	i          int
		retvalue string
	)

	value := zabNotSupported + " Unsupported item key. " + item
	item = strings.Replace(item, "oracle[", "", 1)
	item = strings.Replace(item, "]", "", 1)
	// log.Debug("item = ", item)
	s := strings.Split(item, ",")
	if len(s) < 2 {
		value = value + " item reguest not valid " + item
		log.Error("item reguest not valid ", item)
		return value, err
	}
	itemKey = s[0]
	itemDB = strings.ToUpper(s[1])
	if len(s) == 3 {
		itemParam = s[2]
	}
	// log.Debug("reguest itemKey ", itemKey, " itemKey ", itemDB)
	if _, ok := cfg.DBCFG[itemDB]; !ok {
		errText := "connect to DB " + itemDB + " not configured"
		value = value + " " + errText
		log.Error(errText)
		return value, errors.New(errText)
	}
	if _, ok := itemSQL[itemKey]; !ok {
		errText := "sql not found for " + itemKey
		value = value + " " + errText
		log.Error(errText, " ", item)
		return value, errors.New(errText)
	}
	// выполнять запросы только если есть соединение к БД
	err = dbConn[itemDB].Ping()
	if err != nil {
		if itemKey == "connect" {
			value = "0"
		} else {
			value = value + " no connection to DB " + itemDB + " " + err.Error()
			log.Warn(itemDB, value)
		}
		return value, nil
	}

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second*time.Duration(sqlTimeOut))
	defer cancel()
	if itemParam == "" {
		// !? if query return many rows QueryRow return first row without error
		err = dbConn[itemDB].QueryRowContext(ctx, itemSQL[itemKey]).Scan(&retvalue)
	} else {
		err = dbConn[itemDB].QueryRowContext(ctx, itemSQL[itemKey], itemParam).Scan(&retvalue)
	}
	// rows, err := dbConn[itemDB].Query(itemSQL[itemKey])
	if err != nil {
		sql := strings.Replace(itemSQL[itemKey], "\n", " ", -1) // For format error return to zabbix
		value = value + " error run sql " + sql + " " + err.Error()
		log.Error(value)
	} else {
		value = retvalue
	}
	if itemKey == "connect" && err != nil {
		value = "0" // return 0 for zabbix trigger, else item become unsupported
	}
	return value, err
} // func getDBValue

// Parse and load SQL file
func loadSQLFile(filenames []string) {
	itemSQL = make(map[string]string)
	for _, filename := range filenames {
		log.Info("Parse SQL file - ", filename)
		f, err := os.Open(filename)
		if err != nil {
			log.Fatal("Error read SQLFile file: " + filename + " " + err.Error())
		}
		doc, err := xmlquery.Parse(f)
		if err != nil {
			log.Fatal("Error parse SQLFile file: " + filename + " " + err.Error())
		}
		tmpSQL := make(map[string]string)
		for _, n := range xmlquery.Find(doc, "//sql") {
			// log.Debug("Node ", n, "itemKey ", n.SelectAttr("itemKey"), " ", n.InnerText())
			s := n.SelectAttr("item_key")
			//fmt.Print("s = ")
			//fmt.Print(n.SelectAttr("item_key"))
			//fmt.Println(s)
			tmpSQL[s] = n.InnerText()
			//log.Debug("item key ", s)
			//log.Debug("item key ", tmpSQL[s])
		}
		// merge into itemSQL
		//log.Debug("tmpSQL 1 ", tmpSQL)
		for i, s := range tmpSQL {
			//			log.Debug("tmpSQL 2 ", i, s)
			if _, ok := itemSQL[i]; ok {
				log.Warn("Duplicate Item ", i, " file ", filename)
			} else {
				itemSQL[i] = s
			}
		}
	}
	//log.Debug("itemSQL ", itemSQL)
}
