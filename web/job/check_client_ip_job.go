package job

import (
	"encoding/json"
	"os"
	"regexp"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/web/service"
	"x-ui/xray"

	"net"
	"sort"
	"strings"
	"time"

	"github.com/go-cmd/cmd"
)

type CheckClientIpJob struct {
	xrayService service.XrayService
}

var job *CheckClientIpJob
var disAllowedIps []string

func NewCheckClientIpJob() *CheckClientIpJob {
	job = new(CheckClientIpJob)
	return job
}

func (j *CheckClientIpJob) Run() {
	logger.Debug("Check Client IP Job...")
	processLogFile()

	// disAllowedIps = []string{"192.168.1.183","192.168.1.197"}
	blockedIps := []byte(strings.Join(disAllowedIps, ","))

	// check if file exists, if not create one
	_, err := os.Stat(xray.GetBlockedIPsPath())
	if os.IsNotExist(err) {
		_, err = os.OpenFile(xray.GetBlockedIPsPath(), os.O_RDWR|os.O_CREATE, 0755)
		checkError(err)
	}
	err = os.WriteFile(xray.GetBlockedIPsPath(), blockedIps, 0755)
	checkError(err)
}

func processLogFile() {
	accessLogPath := GetAccessLogPath()
	if accessLogPath == "" {
		logger.Warning("access.log doesn't exist in your config.json")
		return
	}

	data, err := os.ReadFile(accessLogPath)
	InboundClientIps := make(map[string][]string)
	checkError(err)

	// clean log
	if err := os.Truncate(GetAccessLogPath(), 0); err != nil {
		checkError(err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		ipRegx, _ := regexp.Compile(`[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`)
		emailRegx, _ := regexp.Compile(`email:.+`)

		matchesIp := ipRegx.FindString(line)
		if len(matchesIp) > 0 {
			ip := string(matchesIp)
			if ip == "127.0.0.1" || ip == "1.1.1.1" {
				continue
			}

			matchesEmail := emailRegx.FindString(line)
			if matchesEmail == "" {
				continue
			}
			matchesEmail = strings.TrimSpace(strings.Split(matchesEmail, "email: ")[1])

			if InboundClientIps[matchesEmail] != nil {
				if contains(InboundClientIps[matchesEmail], ip) {
					continue
				}
				InboundClientIps[matchesEmail] = append(InboundClientIps[matchesEmail], ip)

			} else {
				InboundClientIps[matchesEmail] = append(InboundClientIps[matchesEmail], ip)
			}
		}

	}
	disAllowedIps = []string{}

	for clientEmail, ips := range InboundClientIps {
		inboundClientIps, err := GetInboundClientIps(clientEmail)
		sort.Strings(ips)
		if err != nil {
			addInboundClientIps(clientEmail, ips)

		} else {
			updateInboundClientIps(inboundClientIps, clientEmail, ips)
		}

	}

	// check if inbound connection is more than limited ip and drop connection
	LimitDevice := func() { LimitDevice() }

	stop := schedule(LimitDevice, 1000*time.Millisecond)
	time.Sleep(10 * time.Second)
	stop <- true

}
func GetAccessLogPath() string {

	config, err := os.ReadFile(xray.GetConfigPath())
	checkError(err)

	jsonConfig := map[string]interface{}{}
	err = json.Unmarshal([]byte(config), &jsonConfig)
	checkError(err)
	if jsonConfig["log"] != nil {
		jsonLog := jsonConfig["log"].(map[string]interface{})
		if jsonLog["access"] != nil {

			accessLogPath := jsonLog["access"].(string)

			return accessLogPath
		}
	}
	return ""

}
func checkError(e error) {
	if e != nil {
		logger.Warning("client ip job err:", e)
	}
}
func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}
func GetInboundClientIps(clientEmail string) (*model.InboundClientIps, error) {
	db := database.GetDB()
	InboundClientIps := &model.InboundClientIps{}
	err := db.Model(model.InboundClientIps{}).Where("client_email = ?", clientEmail).First(InboundClientIps).Error
	if err != nil {
		return nil, err
	}
	return InboundClientIps, nil
}
func addInboundClientIps(clientEmail string, ips []string) error {
	inboundClientIps := &model.InboundClientIps{}
	jsonIps, err := json.Marshal(ips)
	checkError(err)

	inboundClientIps.ClientEmail = clientEmail
	inboundClientIps.Ips = string(jsonIps)

	db := database.GetDB()
	tx := db.Begin()

	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	err = tx.Save(inboundClientIps).Error
	if err != nil {
		return err
	}
	return nil
}
func updateInboundClientIps(inboundClientIps *model.InboundClientIps, clientEmail string, ips []string) error {

	jsonIps, err := json.Marshal(ips)
	checkError(err)

	inboundClientIps.ClientEmail = clientEmail
	inboundClientIps.Ips = string(jsonIps)

	// check inbound limitation
	inbound, err := GetInboundByEmail(clientEmail)
	checkError(err)

	if inbound.Settings == "" {
		logger.Debug("wrong data ", inbound)
		return nil
	}

	settings := map[string][]model.Client{}
	json.Unmarshal([]byte(inbound.Settings), &settings)
	clients := settings["clients"]

	for _, client := range clients {
		if client.Email == clientEmail {

			limitIp := client.LimitIP

			if limitIp < len(ips) && limitIp != 0 && inbound.Enable {

				disAllowedIps = append(disAllowedIps, ips[limitIp:]...)
			}
		}
	}
	logger.Debug("disAllowedIps ", disAllowedIps)
	sort.Strings(disAllowedIps)

	db := database.GetDB()
	err = db.Save(inboundClientIps).Error
	if err != nil {
		return err
	}
	return nil
}
func DisableInbound(id int) error {
	db := database.GetDB()
	result := db.Model(model.Inbound{}).
		Where("id = ? and enable = ?", id, true).
		Update("enable", false)
	err := result.Error
	logger.Warning("disable inbound with id:", id)

	if err == nil {
		job.xrayService.SetToNeedRestart()
	}

	return err
}

func GetInboundByEmail(clientEmail string) (*model.Inbound, error) {
	db := database.GetDB()
	var inbounds *model.Inbound
	err := db.Model(model.Inbound{}).Where("settings LIKE ?", "%"+clientEmail+"%").Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	return inbounds, nil
}

func LimitDevice() {

	localIp, err := LocalIP()
	checkError(err)

	c := cmd.NewCmd("bash", "-c", "ss --tcp | grep -E '"+IPsToRegex(localIp)+"'| awk '{if($1==\"ESTAB\") print $4,$5;}'", "| sort | uniq -c | sort -nr | head")

	<-c.Start()
	if len(c.Status().Stdout) > 0 {
		ipRegx, _ := regexp.Compile(`[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+`)
		portRegx, _ := regexp.Compile(`(?:(:))([0-9]..[^.][0-9]+)`)

		for _, row := range c.Status().Stdout {

			data := strings.Split(row, " ")

			destIp, destPort, srcIp, srcPort := "", "", "", ""

			destIp = string(ipRegx.FindString(data[0]))

			destPort = portRegx.FindString(data[0])
			destPort = strings.Replace(destPort, ":", "", -1)

			srcIp = string(ipRegx.FindString(data[1]))

			srcPort = portRegx.FindString(data[1])
			srcPort = strings.Replace(srcPort, ":", "", -1)

			if contains(disAllowedIps, srcIp) {
				dropCmd := cmd.NewCmd("bash", "-c", "ss -K dport = "+srcPort)
				dropCmd.Start()

				logger.Debug("request droped : ", srcIp, srcPort, "to", destIp, destPort)
			}
		}
	}

}

func LocalIP() ([]string, error) {
	// get machine ips

	ifaces, err := net.Interfaces()
	ips := []string{}
	if err != nil {
		return ips, err
	}
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			return ips, err
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			ips = append(ips, ip.String())

		}
	}
	logger.Debug("System IPs : ", ips)

	return ips, nil
}

func IPsToRegex(ips []string) string {

	regx := ""
	for _, ip := range ips {
		regx += "(" + strings.Replace(ip, ".", "\\.", -1) + ")"

	}
	regx = "(" + strings.Replace(regx, ")(", ")|(.", -1) + ")"

	return regx
}

func schedule(LimitDevice func(), delay time.Duration) chan bool {
	stop := make(chan bool)

	go func() {
		for {
			LimitDevice()
			select {
			case <-time.After(delay):
			case <-stop:
				return
			}
		}
	}()

	return stop
}
