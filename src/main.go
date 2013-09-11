package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sshoperation"
	"strconv"
	"strings"
	"time"
)

// 声明全局变量
var logger *log.Logger
var db *sql.DB
var vip = "192.168.2.201"

// 页面导航栏高亮数据
/*
type navHighlight struct {
	AddTask    string
	ListenList string
}
*/

//index页面，即添加任务页面，模板数据
/*
type indexData struct {
	Nav navHighlight
}
*/

// 状态结果结构体
type statusResult struct {
	Success string
	Msg     string
}

// 主页请求处理函数
func getHomePage(w http.ResponseWriter, r *http.Request) {
	t, err := template.ParseFiles("../template/header.tmpl", "../template/index.tmpl", "../template/footer.tmpl")
	if err != nil {
		fmt.Println(err)
	} else {
		t.ExecuteTemplate(w, "index", nil)
	}
}

// 根据数据库数据重新生成HAProxy配置文件，并重启HAProxy
func rebuildHAProxyConf() {

	// 存储配置文件解析结果
	type config struct {
		Global       []string
		Defaults     []string
		ListenStats  []string
		ListenCommon []string
	}

	newConfigParts := make([]string,0, 50)

	bytes, err := ioutil.ReadFile("../conf/haproxy_conf_common.json")
	//fmt.Println(string(bytes))
	if err != nil {
		return
	}
	var conf config
	err = json.Unmarshal(bytes, &conf)
	if err != nil {
		return
	}
	newConfigParts = append(newConfigParts, strings.Join(conf.Global, "\n\t"))
	newConfigParts = append(newConfigParts, strings.Join(conf.Defaults, "\n\t"))
	newConfigParts = append(newConfigParts, strings.Join(conf.ListenStats, "\n\t"))

	rows, err := db.Query("SELECT servers, vport, logornot FROM haproxymapinfo ORDER BY vport ASC")
	if err != nil {
		logger.Println(err)
	}

	var servers string
	var vport int
	var logOrNot int
	for rows.Next() {
		err = rows.Scan(&servers, &vport, &logOrNot)
		serverList := strings.Split(servers, "-")
		backendServerInfoList := make([]string,0, 10)

		for i := 0; i < len(serverList); i++ {
			backendServerInfoList = append(backendServerInfoList, fmt.Sprintf("server %s %s weight 3 check inter 2000 rise 2 fall 3", serverList[i], serverList[i]))
		}
		listenCommon := conf.ListenCommon
		if logOrNot == 1 {
			logDirective := "option tcplog\n\tlog global\n\t"
			listenCommon = append(listenCommon, logDirective)
		}
		newConfigParts = append(newConfigParts, fmt.Sprintf("listen Listen-%d\n\tbind *:%d\n\t%s\n\n\t%s", vport, vport, strings.Join(listenCommon, "\n\t"), strings.Join(backendServerInfoList, "\n\t")))
	}
	err = rows.Err()
	if err != nil {
		fmt.Println(err)
	}
	newConfig := strings.Join(newConfigParts, "\n\n")
	// 必须使用os.O_TRUNC来清空文件
	haproxyConfFile, err := os.OpenFile("/usr/local/haproxy/conf/haproxy.conf", os.O_CREATE | os.O_RDWR | os.O_TRUNC, 0666)
	if err != nil {
		fmt.Println(err)
	}
	defer haproxyConfFile.Close()
	haproxyConfFile.WriteString(newConfig)
	return
}

// 申请虚拟ip端口请求处理函数
func applyVPort(w http.ResponseWriter, r *http.Request) {

	servers := r.FormValue("servers")
	comment := r.FormValue("comment")
	logOrNot := r.FormValue("logornot")

	rows, err := db.Query("SELECT vport FROM haproxymapinfo ORDER BY vport ASC")
	if err != nil {
		logger.Println(err)
	}
    /*
    虚拟ip端口分配算法
    */
    // 端口占用标志位数组
    var portSlots [10000]bool
    for index := 0; index < 10000; index++ {
        portSlots[index] = false
    }
    portList := make([]int, 0, 500)
	var port int
	for rows.Next() {
		err = rows.Scan(&port)
        portList = append(portList, port)
        portSlots[port-10000] = true
	}
    if err != nil {
        logger.Println(err)
    }
    var vportToAssign int
    portNum := len(portList)
    if portNum == 0 {
        vportToAssign = 10000
    } else {
        maxiumVPort := portList[portNum-1]
	    vportToAssign = maxiumVPort + 1
        if (portNum + 9999) < maxiumVPort {
            boundary := maxiumVPort - 9999
            for index := 0; index < boundary; index++ {
                if(portSlots[index] == false){
                    vportToAssign = index + 10000
                    break
                }
            }
        }
    }

	now := time.Now().Format("2006-01-02 15:04:05")
	_, err = db.Exec("INSERT INTO haproxymapinfo (servers, vport, comment, logornot, datetime) VALUES (?, ?, ?, ?, ?)", servers, vportToAssign, comment, logOrNot, now)
	if err != nil {
		logger.Println(err)
	}
	messageParts := make([]string,0, 2)
	messageParts = append(messageParts, vip)
	messageParts = append(messageParts, strconv.Itoa(vportToAssign))
	message := strings.Join(messageParts, ":")

	result := statusResult{
		Success: "true",
		Msg:     message,
	}
	rt, err := json.Marshal(result)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Fprintf(w, string(rt))
	return
}

// 获取haproxy listen任务列表
func getListenList(w http.ResponseWriter, r *http.Request) {

	// listen任务列表数据
	type listenTaskInfo struct {
		Seq      int
		Id       int
		Servers  template.HTML
		Vip      string
		Vport    int
		Comment  string
		LogOrNot int
		DateTime string
	}
	// listenlist页面模板数据
	type listenListData struct {
		ListenTaskList []listenTaskInfo
	}

	rows, err := db.Query("SELECT id, servers, vport, comment, logornot, datetime FROM haproxymapinfo ORDER BY datetime DESC")
	if err != nil {
		fmt.Println(err)
	}

	var listenTasks = make([]listenTaskInfo,0, 100)
	var id int
	var servers string
	var vport int
	var comment string
	var logOrNot int
	var dateTime string
	seq := 1
	for rows.Next() {
		err = rows.Scan(&id, &servers, &vport, &comment, &logOrNot, &dateTime)
		lti := listenTaskInfo{
			Seq:      seq,
			Id: id,
			Servers:  template.HTML(strings.Join(strings.Split(servers, "-"), "<br />")),
			Vip:      vip,
			Vport:    vport,
			Comment:  comment,
			LogOrNot: logOrNot,
			DateTime: dateTime,
		}
		listenTasks = append(listenTasks, lti)
		seq = seq + 1
	}
	err = rows.Err()
	if err != nil {
		fmt.Println(err)
	}
	Lld := listenListData{
		ListenTaskList: listenTasks,
	}

	t, err := template.ParseFiles("../template/header.tmpl", "../template/listenlist.tmpl", "../template/footer.tmpl")
	if err != nil {
		fmt.Println(err)
	}
	t.ExecuteTemplate(w, "listenlist", Lld)
	return
}

func delListenTask(w http.ResponseWriter, r *http.Request) {

	success := "true"
	msg := "已成功删除"

	vport := r.FormValue("taskvport")
	result, err := db.Exec("DELETE FROM haproxymapinfo WHERE vport=?", vport)
	if err != nil {
		logger.Fatalln(err)
		success = "false"
		msg = "从数据库删除数据出错！"
	}
	rowsAffected, err := result.RowsAffected()
	if rowsAffected != 1 {
		success = "false"
		msg = fmt.Sprintf("数据删除有问题，删除了%d条", rowsAffected)
	}
	rt, _ := json.Marshal(statusResult{Success: success, Msg: msg})
	fmt.Fprintf(w, string(rt))
	return
}

// 编辑任务
func editTask(w http.ResponseWriter, r *http.Request) {

	success := "true"
	msg := "更新成功！"

	servers := r.FormValue("servers")
	comment := r.FormValue("comment")
	logornot := r.FormValue("logornot")
	id := r.FormValue("id")
	now := time.Now().Format("2006-01-02 15:04:05")
	_, err := db.Exec("UPDATE haproxymapinfo SET servers=?, comment=?, logornot=?, datetime=? WHERE id=?", servers, comment, logornot, now, id)
	if err != nil {
		logger.Println(err)
		success = "false"
		msg = "数据库操作出现问题!"
	}
	result := statusResult{
		Success: success,
		Msg:     msg,
	}
	rt, err := json.Marshal(result)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Fprintf(w, string(rt))
	return
}

func applyConf(w http.ResponseWriter, r *http.Request) {

	success := "true"
	msg := "成功应用！"

	target := r.FormValue("target")
	rebuildHAProxyConf()
	if (target == "master") {
		// 重启haproxy
		cmd := exec.Command("/usr/local/haproxy/restart_haproxy.sh")
		err := cmd.Run()
		if err != nil {
			logger.Println(err)
			success = "false"
			msg = fmt.Sprintf("应用失败！%s", err.Error())
		}
	}else {
		err := sshoperation.ScpHaproxyConf()
		if err != nil {
			success = "false"
			msg = fmt.Sprintf("应用失败！%s", err.Error())
		}
	}
	result := statusResult{
		Success: success,
		Msg:     msg,
	}
	rt, err := json.Marshal(result)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Fprintf(w, string(rt))
	return
}

// 日志初始化函数
func getLogger() (logger *log.Logger) {
	os.Mkdir("../log/", 0666)
	logFile, err := os.OpenFile("../log/HAProxyConsole.log", os.O_CREATE | os.O_RDWR | os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
	logger = log.New(logFile, "\r\n", log.Ldate | log.Ltime | log.Lshortfile)
	return
}

func main() {

	var err error
	logger = getLogger()

	// 数据库连接初始化
	db, err = sql.Open("mysql", "root:06122553@tcp(127.0.0.1:3306)/haproxyconsole?charset=utf8")
	if err != nil {
		logger.Fatalln(err)
		os.Exit(1)
	}
	defer db.Close()

	// 请求路由
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../static/"))))
	http.HandleFunc("/applyvport", applyVPort)
	http.HandleFunc("/edittask", editTask)
	http.HandleFunc("/listenlist", getListenList)
	http.HandleFunc("/dellistentask", delListenTask)
	http.HandleFunc("/applyconf", applyConf)
	http.HandleFunc("/", getHomePage)

	// 启动http服务
	err = http.ListenAndServe(":9090", nil)
	if err != nil {
		logger.Fatalln("ListenAndServe: ", err)
	}
}
