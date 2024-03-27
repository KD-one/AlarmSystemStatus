package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/spf13/viper"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Content       string   `json:"content"`
	MentionedList []string `json:"mentioned_list"`
}

type NetworkUsage struct {
	Name        string
	Received    uint64
	Transmitted uint64
}

var (
	cpuLastWarningTime         time.Time // 声明后使用时，值是0001-01-01 00:00:00 +0000 UTC   IsZero() = true
	memoryLastWarningTime      time.Time
	prevInterfaces             = make(map[string]NetworkUsage)
	receivedLastWarningTime    time.Time
	transmittedLastWarningTime time.Time
)

// SendWeChatAlarm 发送企业微信告警
func SendWeChatAlarm(url, content string, atUsers ...string) error {
	// 如果需要@所有人，需要在atUsers中添加 "@all",
	// 需要@某个人直接添加其企业微信userid即可(一般为名字的英文拼写,全部小写)
	var message Message

	param := make(map[string]interface{})
	param["msgtype"] = "text"

	message.Content = content
	message.MentionedList = atUsers

	param["text"] = message

	postData, er := json.Marshal(param)
	if er != nil {
		return errors.New("json marshal出错")
	}

	resp, err := http.Post(url, "application/json", strings.NewReader(string(postData)))
	if err != nil {
		return errors.New("发送消息出错")
	}
	defer resp.Body.Close()

	// 如果需要，可以进一步检查响应内容
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.New("读取响应内容出错")
	}
	fmt.Println("发送消息后的响应: ", string(bodyBytes))
	return nil
}

// CompareMemoryUsedPercent 检查内存使用率
// 参数: limit: 阈值, limitDuration: 警告间隔
// 功能：第一次大于阈值时企业微信告警，如果保持超过阈值状态limitDuration后再次告警。如果在limitDuration内使用率降低到阈值以下后，再次升高到阈值以上时重新报和第一次超过时同样的警报
func CompareMemoryUsedPercent(limit float64, limitDuration time.Duration) error {

	v, err := mem.VirtualMemory()
	if err != nil {
		return errors.New(fmt.Sprint("获取内存使用率出错: ", err.Error()))
	}
	err = ValidAlert(viper.GetString("robot.monitor"), "内存使用率", limit, v.UsedPercent, limitDuration, &memoryLastWarningTime)
	if err != nil {
		return err
	}
	fmt.Println("内存使用率: ", v.UsedPercent)
	return nil
}

// CompareCpuUsedPercent 检查cpu使用率
// 参数: limit: 阈值, limitDuration: 警告间隔
// 功能：第一次大于阈值时企业微信告警，如果保持超过阈值状态limitDuration后再次告警。如果在limitDuration内使用率降低到阈值以下后，再次升高到阈值以上时重新报和第一次超过时同样的警报
func CompareCpuUsedPercent(limit float64, limitDuration time.Duration) error {

	percent, err := cpu.Percent(0, false)
	if err != nil {
		return errors.New(fmt.Sprint("获取cpu使用率出错: ", err.Error()))
	}
	err = ValidAlert(viper.GetString("robot.monitor"), "cpu使用率", limit, percent[0], limitDuration, &cpuLastWarningTime)
	if err != nil {
		return err
	}
	fmt.Println("cpu使用率: ", percent[0])
	return nil
}

// CompareNetUsedPercent 检查网络带宽使用率
// 参数: limit: 阈值, limitDuration: 警告间隔
// 功能：第一次大于阈值时企业微信告警，如果保持超过阈值状态limitDuration后再次告警。如果在limitDuration内使用率降低到阈值以下后，再次升高到阈值以上时重新报和第一次超过时同样的警报
func CompareNetUsedPercent(receivedLimit, transmittedLimit uint64, limitDuration time.Duration) error {
	// 获取所有网络接口的流量信息
	interfaces, err := net.IOCounters(false)
	if err != nil {
		return errors.New(fmt.Sprint("获取网络接口流量信息出错: ", err.Error()))
	}
	// 遍历所有接口
	iface := interfaces[0]
	// 提取出接口名称和流量信息
	name := iface.Name
	receivedBytes := iface.BytesRecv
	transmittedBytes := iface.BytesSent

	// 获取上一次的流量信息
	prevUsage, ok := prevInterfaces[name]
	if !ok {
		prevInterfaces[name] = NetworkUsage{Name: name, Received: receivedBytes, Transmitted: transmittedBytes}
	}

	// 计算带宽使用率（每秒）单位：Mbps
	intervalReceived := (receivedBytes - prevUsage.Received) / 131072
	intervalTransmitted := (transmittedBytes - prevUsage.Transmitted) / 131072

	err = ValidAlertNet(viper.GetString("robot.monitor"), receivedLimit, transmittedLimit, intervalReceived, intervalTransmitted, limitDuration, &receivedLastWarningTime, &transmittedLastWarningTime)
	if err != nil {
		return err
	}

	prevInterfaces[name] = NetworkUsage{Name: name, Received: receivedBytes, Transmitted: transmittedBytes}
	// 输出相关信息
	fmt.Printf("网络接收使用率: %d\n网络传输使用率: %d\n\n\n", intervalReceived, intervalTransmitted)

	return nil
}

// ValidAlert 验证当前是否需要告警
func ValidAlert(robotName, alert string, limit, validData float64, limitDuration time.Duration, lastWarningTime *time.Time) error {
	now := time.Now()

	// 内存使用率超过报警线
	if validData > limit {
		// 第一次触发警告,重复计时
		if lastWarningTime.IsZero() {
			*lastWarningTime = now
			err := SendWeChatAlarm(robotName, fmt.Sprintf("%s高于阈值: %.2f", alert, limit))
			if err != nil {
				return err
			}
			// 距离上次警告已经超过30分钟，重复计时
		} else if now.Sub(*lastWarningTime) >= limitDuration {
			*lastWarningTime = now
			err := SendWeChatAlarm(robotName, fmt.Sprintf("%s持续30分钟高于阈值: %.2f", alert, limit))
			if err != nil {
				return err
			}
		}
		// 内存使用率在limit以下
	} else if lastWarningTime != nil && now.Sub(*lastWarningTime) < limitDuration {
		// 内存使用率从大于limit降到了limit以下，重置最后警告时间
		// 不可以使用 lastWarningTime = &time.Time{} !!!!!!!!!
		*lastWarningTime = time.Time{}
	}
	return nil
}

func ValidAlertNet(robotName string, receivedLimit, transmittedLimit, intervalReceived, intervalTransmitted uint64, limitDuration time.Duration, receivedLastWarningTime, transmittedLastWarningTime *time.Time) error {
	now := time.Now()

	// 网络接收使用率超过报警线
	if intervalReceived > receivedLimit {
		// 第一次触发警告,重复计时
		if receivedLastWarningTime.IsZero() {
			*receivedLastWarningTime = now
			err := SendWeChatAlarm(robotName, fmt.Sprintf("网络接收使用率高于阈值: %d", receivedLimit))
			if err != nil {
				return err
			}
			// 距离上次警告已经超过30分钟，重复计时
		} else if now.Sub(*receivedLastWarningTime) >= limitDuration {
			*receivedLastWarningTime = now
			err := SendWeChatAlarm(robotName, fmt.Sprintf("网络接收使用率持续30分钟高于阈值: %d", receivedLimit))
			if err != nil {
				return err
			}
		}
		// 内存使用率在limit以下
	} else if !receivedLastWarningTime.IsZero() && now.Sub(*receivedLastWarningTime) < limitDuration {
		// 内存使用率从大于limit降到了limit以下，重置最后警告时间
		// 不可以使用 lastWarningTime = &time.Time{} !!!!!!!!!
		*receivedLastWarningTime = time.Time{}
	}

	// 网络传输使用率超过报警线
	if intervalTransmitted > transmittedLimit {
		// 第一次触发警告,重复计时
		if transmittedLastWarningTime.IsZero() {
			*transmittedLastWarningTime = now
			err := SendWeChatAlarm(robotName, fmt.Sprintf("网络传输使用率高于阈值: %d", transmittedLimit))
			if err != nil {
				return err
			}
			// 距离上次警告已经超过30分钟，重复计时
		} else if now.Sub(*transmittedLastWarningTime) >= limitDuration {
			*transmittedLastWarningTime = now
			err := SendWeChatAlarm(robotName, fmt.Sprintf("网络传输使用率持续30分钟高于阈值: %d", transmittedLimit))
			if err != nil {
				return err
			}
		}
		// 内存使用率在limit以下
	} else if transmittedLastWarningTime != nil && now.Sub(*transmittedLastWarningTime) < limitDuration {
		// 内存使用率从大于limit降到了limit以下，重置最后警告时间
		// 不可以使用 lastWarningTime = &time.Time{} !!!!!!!!!
		*transmittedLastWarningTime = time.Time{}
	}
	return nil
}

func CheckSystemStatus() {
	for {
		_ = CompareMemoryUsedPercent(viper.GetFloat64("system.memoryLimit"), viper.GetDuration("system.repeatAlarmInterval")*time.Second)
		_ = CompareCpuUsedPercent(viper.GetFloat64("system.cpuLimit"), viper.GetDuration("system.repeatAlarmInterval")*time.Second)
		_ = CompareNetUsedPercent(viper.GetUint64("system.receivedLimit"), viper.GetUint64("system.transmittedLimit"), viper.GetDuration("system.repeatAlarmInterval")*time.Second)
		time.Sleep(time.Second * viper.GetDuration("system.checkInterval"))
	}
}
