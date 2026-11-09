//go:build windows

package main

// 复制/剪切文件：把文件列表（CF_HDROP）写入剪贴板，并附带“Preferred DropEffect”
// 声明粘贴语义，资源管理器粘贴时据此执行复制或移动。

import (
	"encoding/binary"

	"github.com/egoist/mygo/transfer"
)

// preferredDropEffect 即 CFSTR_PREFERREDDROPEFFECT：DWORD 取值
// DROPEFFECT_COPY（1）或 DROPEFFECT_MOVE（2）。
const preferredDropEffect = transfer.Format("Preferred DropEffect")

func fileClipboardData(path string, cut bool) (transfer.Data, error) {
	data, err := transfer.FileData(path)
	if err != nil {
		return transfer.Data{}, err
	}
	effect := uint32(1) // DROPEFFECT_COPY
	if cut {
		effect = 2 // DROPEFFECT_MOVE
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], effect)
	items := append(data.Items(), transfer.NewItem(transfer.Bytes(preferredDropEffect, raw[:])))
	return transfer.New(items...), nil
}
