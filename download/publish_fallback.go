package download

// 发布回退：跨卷或文件系统不支持硬链接时，用复制完成不覆盖发布；替换则
// 复制到同目录临时文件后改名，避免半成品目标。

import (
	"io"
	"os"
)

// copyPublishedFile 复制发布；目标已存在时不覆盖，成功后删除临时文件。
func copyPublishedFile(part, destination string) error {
	in, err := os.Open(extendedPath(part))
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(extendedPath(destination), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(extendedPath(destination))
		return &os.LinkError{Op: "copy", Old: part, New: destination, Err: err}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(extendedPath(destination))
		return &os.LinkError{Op: "copy", Old: part, New: destination, Err: err}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(extendedPath(destination))
		return &os.LinkError{Op: "copy", Old: part, New: destination, Err: err}
	}
	// 完整目标已经可用；删除临时文件失败不当作下载失败。
	_ = os.Remove(extendedPath(part))
	return nil
}

// replaceByCopy 复制到同目录临时文件后改名替换；用于跨卷的原子替换回退。
func replaceByCopy(part, destination string) error {
	temporary := destination + ".publishing"
	_ = os.Remove(extendedPath(temporary))
	if err := copyPublishedFile(part, temporary); err != nil {
		_ = os.Remove(extendedPath(temporary))
		return err
	}
	if err := os.Rename(extendedPath(temporary), extendedPath(destination)); err != nil {
		_ = os.Remove(extendedPath(temporary))
		return &os.LinkError{Op: "replace", Old: part, New: destination, Err: err}
	}
	return nil
}
