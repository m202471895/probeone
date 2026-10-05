package collect

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// collectSensors 采集温度与风扇转速。
//
// 实现方式：读 Linux sysfs，不引入额外依赖。
// 非 Linux 平台返回空——温度传感器在容器与云主机里通常不可见，
// 与其返回一个编造的 0℃ 让人误判，不如诚实地不报。
//
// 权限说明：读 /sys/class/hwmon 需要 root 或 hwmon 组权限。
// 低权运行时会返回空切片，Agent 照常工作（PRD 9.2A8 要求无特权可跑核心指标）。
func collectSensors() []Sensor {
	if runtime.GOOS != "linux" {
		return nil
	}
	base := "/sys/class/hwmon"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}

	var out []Sensor
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "hwmon") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		chipName := readSysfsString(filepath.Join(dir, "name"))

		// 温度：tempN_input（毫摄氏度）
		out = append(out, readSensorsOfType(dir, "temp", "temperature", "°C", chipName)...)
		// 风扇：fanN_input（RPM）
		out = append(out, readSensorsOfType(dir, "fan", "fan", "RPM", chipName)...)
	}
	return out
}

// readSensorsOfType 读取某一类传感器（temp 或 fan）。
func readSensorsOfType(dir, prefix, kind, unit, chip string) []Sensor {
	var out []Sensor
	for i := 0; i < 32; i++ {
		inputPath := filepath.Join(dir, prefix+strconv.Itoa(i)+"_input")
		raw, err := os.ReadFile(inputPath)
		if err != nil {
			// 编号不连续，遇到第一个不存在的就停
			break
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil {
			continue
		}

		// temp 的单位是毫摄氏度
		if prefix == "temp" {
			value /= 1000
		}

		// 优先用标签名（CPU Package / Core 0 之类），没有就用编号
		name := chip
		label := readSysfsString(filepath.Join(dir, prefix+strconv.Itoa(i)+"_label"))
		if label != "" {
			if chip != "" && !strings.EqualFold(label, chip) {
				name = chip + " " + label
			} else {
				name = label
			}
		} else {
			name = chip + " " + prefix + strconv.Itoa(i)
		}

		// 0 值通常是"读取失败"的占位，不上报
		if value <= 0 {
			continue
		}

		out = append(out, Sensor{
			Name:  name,
			Kind:  kind,
			Value: round2(value),
			Unit:  unit,
		})
	}
	return out
}

// readSysfsString 读 sysfs 里的单行文本文件，失败返回空串。
func readSysfsString(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}
