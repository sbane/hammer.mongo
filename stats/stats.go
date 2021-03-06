package stats

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/bmizerany/perks/quantile"
)

var HammerStats Stats
var _csv_file *os.File

var slowThreshold uint64 // in ms

type GetProfileCSV func(total_time float64) string // to return current profile's stats for CSV
type GetProfileCSVHeader func() string             // to return current profile's header for CSV

var _profileCSV GetProfileCSV
var _profileCSVHeader GetProfileCSVHeader

var csv_header string
var csv_last_Line string

var printedLineNum int64

var IN_WARMUP bool
var silent bool
var runId string
var showPercentile bool = false
var _num_of_workers int

var _env_no_serverStatus bool

var AllStats []Stats

const _stats_use_channel bool = false
const STAT_BATCH_SIZE uint64 = 1 // batch 20 at one time before submit

// Stats will be an atomic, to count the number of request handled
type Stats struct {
	quants        *quantile.Stream
	totalResp     uint64 // total # of request
	totalRespTime uint64 // total response time
	totalErr      uint64 // how many error
	totalResSlow  uint64 // how many slow response
	totalSend     uint64

	lastSend     uint64
	lastResp     uint64
	lastRespTime uint64

	lastTS uint64

	firstTS uint64

	monitor *time.Ticker
	// pstat   ProfileStats

	C_response chan uint64
	C_send     chan uint64
	once       sync.Once
	mu         sync.Mutex
}

// increase the count and record response time.
func (c *Stats) RecordRes(_time uint64, method string, worker_id int) {
	if _stats_use_channel {
		c.C_response <- _time
	} else {
		atomic.AddUint64(&c.totalResp, STAT_BATCH_SIZE)
		atomic.AddUint64(&c.totalRespTime, _time)
	}

	// if longer that 200ms, it is a slow response
	if _time > slowThreshold*1000000 {
		atomic.AddUint64(&c.totalResSlow, 1)
	}

	// record percentile
	//c.mu.Lock()
	//c.quants.Insert(float64(_time))
	//c.mu.Unlock()
}

func (c *Stats) RecordError(worker_id int) {
	atomic.AddUint64(&HammerStats.totalErr, 1)
}

func (c *Stats) RecordSend(worker_id int) uint64 {
	if HammerStats.firstTS == 0 {
		HammerStats.firstTS = uint64(time.Now().UnixNano())
	}

	if _stats_use_channel {
		HammerStats.C_send <- 1
	} else {
		sent := atomic.AddUint64(&c.totalSend, STAT_BATCH_SIZE)
		return sent
	}
	return c.totalSend // this is not accurate, but fairly close
}

func (c *Stats) monitorHammer() {
	ts := uint64(time.Now().UnixNano())

	_total_time := (float64(ts-c.firstTS) / 1.0e9)
	sendps := float64(c.totalSend) / _total_time
	respps := float64(c.totalResp) / _total_time

	// backlog := uint64(c.totalSend - c.totalResp - c.totalErr)
	// log.Println("total", c.totalSend, "resp", c.totalResp)

	avgT := float64(c.totalRespTime) / (float64(c.totalResp) * 1.0e9)
	avgLastT := float64(c.totalRespTime-c.lastRespTime) / (float64(c.totalResp-c.lastResp) * 1.0e9)

	lastSend := c.totalSend - c.lastSend
	atomic.StoreUint64(&c.lastResp, c.totalResp)
	atomic.StoreUint64(&c.lastSend, c.totalSend)
	atomic.StoreUint64(&c.lastRespTime, c.totalRespTime)

	if c.lastTS == 0 {
		c.lastTS = ts
		if !_env_no_serverStatus {
			HammerMongoStats.MonitorMongo()
		}
		return
	}

	c.lastTS = ts
	var s_print string
	var s_csv string

	s_print = ""
	s_print_title := ""
	s_csv = ""
	if !_env_no_serverStatus {
		s_print, s_csv = HammerMongoStats.MonitorMongo()
	}

	/*c.mu.Lock()
	p99 := c.quants.Query(0.99)
	p97 := c.quants.Query(0.97)
	p95 := c.quants.Query(0.95)
	p85 := c.quants.Query(0.85)
	p75 := c.quants.Query(0.75)
	p65 := c.quants.Query(0.65)
	p50 := c.quants.Query(0.50)
	p35 := c.quants.Query(0.35)
	p15 := c.quants.Query(0.15)
	c.mu.Unlock()
	*/

	if !silent {
		var tabWriter *tabwriter.Writer
		var tabBuffer bytes.Buffer
		tabWriter = new(tabwriter.Writer)
		tabWriter.Init(&tabBuffer, 5, 0, 1, ' ', tabwriter.AlignRight)

		// print title
		fmt.Fprintln(tabWriter,
			"time \t",
			"Total send\t",
			"req/s\t",
			"ack/s\t",
			"avg(ms)\t",
			//"p99\t",
			//"p97\t",
			//"p95\t",
			//"p85\t",
			//"p75\t",
			//"p65\t",
			//"p50\t",
			//"p35\t",
			//"p15\t",
			// " pending\t", backlog,
			"err\t",
			//"|", fmt.Sprintf("%2.2f%s", (float64(c.totalErr)*100.0/float64(c.totalErr+c.totalResp)), "%"),
			//" slow\t", fmt.Sprintf("%2.2f%s", (float64(c.totalResSlow)*100.0/float64(c.totalResp)), "%"),
			"Last avg(ms)\t",
			"Last send\t",
			s_print_title)

		// print data
		fmt.Fprintln(tabWriter,
			time.Now().Format(time.Stamp), "\t",
			fmt.Sprintf("%4d\t", c.totalSend),
			fmt.Sprintf("%4.1f\t", sendps),
			fmt.Sprintf("%4.1f\t", respps),
			fmt.Sprintf("%6.3f\t", avgT*1000), // adjust to MS
			//fmt.Sprintf("%6.2f\t", p99/1.0e6),
			//fmt.Sprintf("%6.2f\t", p97/1.0e6),
			//fmt.Sprintf("%6.2f\t", p95/1.0e6),
			//fmt.Sprintf("%6.2f\t", p85/1.0e6),
			//fmt.Sprintf("%6.2f\t", p75/1.0e6),
			//fmt.Sprintf("%6.2f\t", p65/1.0e6),
			//fmt.Sprintf("%6.2f\t", p50/1.0e6),
			//fmt.Sprintf("%6.2f\t", p35/1.0e6),
			//fmt.Sprintf("%6.2f\t", p15/1.0e6),
			fmt.Sprintf("%4d\t", c.totalErr),
			fmt.Sprintf("%6.3f\t", avgLastT*1000),
			fmt.Sprintf("%d\t", lastSend),
			s_print)

		tabWriter.Flush()
		if printedLineNum == 0 {
			fmt.Println()
			fmt.Println(string(bytes.Split(tabBuffer.Bytes(), []byte{byte('\n')})[0]))
		}
		fmt.Println(string(bytes.Split(tabBuffer.Bytes(), []byte{byte('\n')})[1]))

		printedLineNum = printedLineNum + 1

		if printedLineNum == 5 {
			printedLineNum = 0
		}

		//WriteTo(os.Stdout)
	}

	// shall monitor Mongo Here to make sure display is consistant
	// mongoStats.monitorMongo()

	csv_last_Line = fmt.Sprint(
		time.Now().Format(time.Stamp),
		",", fmt.Sprintf("%d", c.totalSend), //Total send:
		",", fmt.Sprintf("%f", sendps), //req/s:
		",", fmt.Sprintf("%f", respps), //ack/s
		",", fmt.Sprintf("%f", avgT*1000), // total avg response time
		//",", fmt.Sprintf("%f", p99/1.0e6),
		//",", fmt.Sprintf("%f", p97/1.0e6),
		//",", fmt.Sprintf("%f", p95/1.0e6),
		//",", fmt.Sprintf("%f", p85/1.0e6),
		//",", fmt.Sprintf("%f", p75/1.0e6),
		//",", fmt.Sprintf("%f", p65/1.0e6),
		//",", fmt.Sprintf("%f", p50/1.0e6),
		// ",", backlog, // backlog
		",", c.totalErr, // total error
		",", fmt.Sprintf("%2.2f", (float64(c.totalErr)*100.0/float64(c.totalErr+c.totalResp))), // error ratio (%)
		",", c.totalResSlow, // total slow
		",", fmt.Sprintf("%2.2f", (float64(c.totalResSlow)*100.0/float64(c.totalResp))), // slow ratio (%)
		",", fmt.Sprintf("%f", avgLastT*1000), // | Last avg:
		",", lastSend, // last Send
		",", s_csv, // output from MongoMonitor
		",", _profileCSV(_total_time))

	_csv_file.WriteString(csv_last_Line + "\n")
}

func PrettyPrint() {
	// to pretty print output
	fmt.Println("\nFinal results:\n")
	h := strings.Split(csv_header, ",")
	l := strings.Split(csv_last_Line, ",")

	for index, _ := range l {
		if h[index] != "NA" && h[index] != "" {
			fmt.Printf("%40s : %s\n", h[index], l[index])
		}
	}
	/*
		fmt.Printf("%40s : %6.2f\n", "p99", HammerStats.quants.Query(0.99))
		fmt.Printf("%40s : %6.2f\n", "p97", HammerStats.quants.Query(0.97))
		fmt.Printf("%40s : %6.2f\n", "p95", HammerStats.quants.Query(0.95))
		fmt.Printf("%40s : %6.2f\n", "p85", HammerStats.quants.Query(0.85))
		fmt.Printf("%40s : %6.2f\n", "p75", HammerStats.quants.Query(0.75))
		fmt.Printf("%40s : %6.2f\n", "p65", HammerStats.quants.Query(0.65))
		fmt.Printf("%40s : %6.2f\n", "p50", HammerStats.quants.Query(0.50))
		fmt.Printf("%40s : %6.2f\n", "p35", HammerStats.quants.Query(0.35))
		fmt.Printf("%40s : %6.2f\n", "p15", HammerStats.quants.Query(0.15))
	*/
}

func (c *Stats) StartMonitoring(monitor_channel *time.Ticker) {

	// this shall run only one
	f := func() {
		HammerStats.monitor = monitor_channel

		// this is the routine to pring stats
		go func() {
			// init percentile here

			for {
				<-HammerStats.monitor.C // rate limit for monitor routine
				if !IN_WARMUP {
					HammerStats.monitorHammer()
				}
			}
		}()

	}

	HammerStats.once.Do(f)
}

func InitProfileStat(h GetProfileCSVHeader, c GetProfileCSV) {
	// init csv log file

	var err error
	var csvFileName string

	if runId == "" {
		csvFileName = "perf_test_data.csv"
	} else {
		csvFileName = "./test_reports/" + runId + "/perf_test_data.csv"
	}

	_csv_file, err = os.Create(csvFileName)
	if err != nil {
		log.Fatalln("Erro open output csv file, error -> ", err)
	}

	// FIXME: a hacky way, or not?, need improvement
	_profileCSV = c
	_profileCSVHeader = h

	// write header
	if showPercentile {
		csv_header = fmt.Sprint("timestamp,total send,req/s,ack/s,avg(ms),p99,p97,p95,p85,p75,p65,p50,total err,err ratio(%),total slow,slow ratio(%),last avg(ms),last sent,") +
			HammerMongoStats.CsvHeader() + "," + _profileCSVHeader()
	} else {
		csv_header = fmt.Sprint("timestamp,total send,req/s,ack/s,avg(ms),total err,err ratio(%),total slow,slow ratio(%),last avg(ms),last sent,") +
			HammerMongoStats.CsvHeader() + "," + _profileCSVHeader()
	}

	_csv_file.WriteString(csv_header + "\n")
}

func SetSilent(s bool) {
	silent = s
}

func SetRunId(_id string) {
	runId = _id
}

func SetNumWorker(_w int) {
	_num_of_workers = _w
	AllStats = make([]Stats, _num_of_workers)
}

func GetRunId() string {
	return runId
}

func init() {
	_env_no_serverStatus = true
	slowThreshold = 2000 // in ms

	// fmt.Println("Init Stats")

	s := os.Getenv("HT_MONGOD_MONITOR")
	if s != "" {
		_env_no_serverStatus = false
	}

	// init channel here
	HammerStats.C_send = make(chan uint64, 1000)     // need optimze this buffer size
	HammerStats.C_response = make(chan uint64, 1000) // need optimze this buffer size
	HammerStats.quants = quantile.NewTargeted(0.15, 0.35, 0.50, 0.85, 0.95, 0.97, 0.99)

	// gorouting to collect send
	go func() {
		for {
			<-HammerStats.C_send
			HammerStats.totalSend += STAT_BATCH_SIZE
		}
	}()

	go func() {
		var r uint64
		for {
			r = <-HammerStats.C_response
			HammerStats.totalResp += STAT_BATCH_SIZE
			HammerStats.totalRespTime += r
		}
	}()
}
