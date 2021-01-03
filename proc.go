package main

import (
	"fmt"
	"github.com/mitchellh/go-ps"
	"io/ioutil"
	"strconv"
	"strings"
	"time"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func GetProcesses(processName string) int {
	p, _ := ps.Processes()

	for _, p1 := range p {
		if p1.Executable() == processName {
			return p1.Pid()
		}
	}
	return 0

}
func GetProcessStats(processName string) map[string]string {
	m := make(map[string]string)
	pid := GetProcesses(processName)
	if pid == 0 {
		return m
	}
	statFilename := "/proc/" + strconv.Itoa(pid) + "/stat"
	dat, err := ioutil.ReadFile(statFilename)
	check(err)
	//fmt.Print(string(dat))
	s := strings.Split(string(dat), " ")
	//fmt.Println(s[10])
	//pidd := s[0]
	utime := s[13]
	ktime := s[14]
	//	vsize := s[22]
	//	rsize := s[23]

	//fmt.Println("pid", pidd, "utime: ", utime, "ktime:", ktime, "vsize", vsize, "rsize", rsize)

	statmFilename := "/proc/" + strconv.Itoa(pid) + "/statm"
	dat, err = ioutil.ReadFile(statmFilename)
	check(err)
	//fmt.Print(string(dat))
	sm := strings.Split(string(dat), " ")
	vsizem := sm[0]
	rsizem := sm[1]
	//	datam := sm[5]

	//fmt.Println("vsizem", vsizem, "rsizem", rsizem, "datam", datam)
	m["vsizem"] = vsizem
	m["rsizem"] = rsizem
	m["utime"] = utime
	m["ktime"] = ktime
	return m
}

func main() {
	for {
		m := GetProcessStats("python2")
		fmt.Println("utime:", m["utime"], "ktime:", m["ktime"], "vsize:", m["vsizem"], "rsizem", m["rsizem"])
		time.Sleep(1 * time.Second)
	}
}
