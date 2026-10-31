package main

import (
	"fmt"
	"os"
	"path/filepath"

	"openDL/download"
	"openDL/style"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (pg *mainPage) requestDelete(rows []d1Row) {
	if len(rows) == 0 || pg.deleteBusy {
		return
	}
	pg.deleteRows = append([]d1Row(nil), rows...)
	pg.deleteFiles = true
	pg.showDelete = true
}

func (pg *mainPage) deleteDialog(c *ui.Context, p *d1Palette) {
	c.Key("delete-dialog")
	ui.DialogBase(c, &pg.showDelete, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, .42)).Center()
		panel.Width(400).Padding(20).Radius(style.RadiusLG).Border(1, p.bd2).Background(p.bg).Label(tr("删除任务"))
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, tr("删除任务")).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
			ui.Text(c, fmt.Sprintf(tr("确定删除选中的 %d 个任务？"), len(pg.deleteRows))).FontSize(style.TextBase).TextColor(p.fg)
			d1Check(c, p, &pg.deleteFiles, "同时删除已完成的本地文件")
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				d1Button(c, p, "取消", "").OnClick(func() { pg.showDelete = false })
				d1PrimaryButton(c, p, "删除", "").OnClick(pg.confirmDelete)
			})
		})
	})
}

// trashLocalFile 把本地文件移入回收站；文件已不存在视为成功。
func trashLocalFile(path string) error {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	// A file may disappear between the initial check and the recycle-bin
	// operation; in that case there is nothing left to remove.
	err = mygo.Shell.TrashItem(path)
	if err != nil {
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			err = nil
		}
	}
	return err
}

func (pg *mainPage) confirmDelete() {
	current := make(map[string]d1Row)
	for _, row := range pg.allRows() {
		current[row.id] = row
	}
	var rows []d1Row
	for _, original := range pg.deleteRows {
		if row, ok := current[original.id]; ok {
			rows = append(rows, row)
		}
	}
	deleteFiles := pg.deleteFiles
	pg.showDelete = false
	if len(rows) == 0 {
		return
	}
	if !deleteFiles {
		for _, row := range rows {
			pg.removeRow(row)
		}
		pg.flash(fmt.Sprintf(tr("已删除 %d 个任务，文件保留在磁盘"), len(rows)))
		return
	}
	// Remove every requested task independently of whether it has a file.
	// Recycling files can take time or fail, but must not block clearing rows.
	for _, row := range rows {
		pg.removeRow(row)
	}
	if pg.win == nil {
		pg.flash(fmt.Sprintf(tr("已删除 %d 个任务"), len(rows)) + "；" + tr("无法删除本地文件"))
		return
	}
	pg.deleteBusy = true
	win := pg.win
	go func() {
		failed := make(map[string]error)
		paths := make(map[string]error)
		for _, row := range rows {
			if row.pending || row.snap.State != download.Done || row.snap.FilePath == "" {
				continue
			}
			path := filepath.Clean(row.snap.FilePath)
			if err, ok := paths[path]; ok {
				if err != nil {
					failed[row.id] = err
				}
				continue
			}
			err := trashLocalFile(path)
			paths[path] = err
			if err != nil {
				failed[row.id] = err
			}
		}
		win.Update(func() {
			pg.deleteBusy = false
			if len(failed) > 0 {
				pg.flash(fmt.Sprintf(tr("已删除 %d 个任务；%d 个文件无法移至回收站"), len(rows), len(failed)))
			} else {
				pg.flash(fmt.Sprintf(tr("已删除 %d 个任务"), len(rows)))
			}
		})
	}()
}
