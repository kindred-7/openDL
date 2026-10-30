package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"openDL/download"
)

type proxySecretPayload struct {
	Version  int    `json:"version"`
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func sealProxyPassword(config download.ProxyConfig) (string, error) {
	data, err := json.Marshal(proxySecretPayload{Version: 1, Address: config.Address, Username: config.Username, Password: config.Password})
	if err != nil {
		return "", err
	}
	defer clear(data)
	if len(data) > 16<<10 {
		return "", fmt.Errorf(tr("代理凭据过长"))
	}
	sealed, err := encryptProxySecret(data)
	if err != nil {
		return "", fmt.Errorf(tr("无法安全保存代理密码：%w"), err)
	}
	return "dpapi1:" + base64.StdEncoding.EncodeToString(sealed), nil
}

func openProxyPassword(config download.ProxyConfig, protected string) (string, error) {
	if !strings.HasPrefix(protected, "dpapi1:") || len(protected) > 128<<10 {
		return "", fmt.Errorf(tr("已保存的代理密码格式无效，请重新输入"))
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(protected, "dpapi1:"))
	if err != nil || len(sealed) == 0 {
		return "", fmt.Errorf(tr("已保存的代理密码格式无效，请重新输入"))
	}
	data, err := decryptProxySecret(sealed)
	if err != nil {
		return "", fmt.Errorf(tr("无法读取已保存的代理密码，请在原 Windows 账户使用或重新输入"))
	}
	defer clear(data)
	var payload proxySecretPayload
	if len(data) > 16<<10 || json.Unmarshal(data, &payload) != nil || payload.Version != 1 || payload.Address != config.Address || payload.Username != config.Username {
		return "", fmt.Errorf(tr("代理密码与当前地址或用户名不匹配，请重新输入"))
	}
	config.Password = payload.Password
	if _, err := download.NormalizeProxyConfig(config); err != nil {
		return "", fmt.Errorf(tr("已保存的代理凭据无效，请重新输入"))
	}
	return payload.Password, nil
}
