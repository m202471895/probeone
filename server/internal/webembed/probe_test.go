package webembed

import (
	"io/fs"
	"testing"
)

// TestEmbeddedFiles 列出实际内嵌的关键文件。
//
// 这个测试存在的原因：go:embed all:dist 对子目录的递归行为
// 需要实测确认——之前 Agent 二进制没被内嵌，表现为下载 404，
// 但编译无任何警告，只能靠枚举清单发现。
func TestEmbeddedFiles(t *testing.T) {
	fsys, err := FS()
	if err != nil {
		t.Fatalf("打开内嵌 FS 失败: %v", err)
	}

	var found []string
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历失败: %v", err)
	}

	t.Logf("共内嵌 %d 个文件", len(found))
	downloads := 0
	for _, f := range found {
		if len(f) > 10 && f[:10] == "downloads/" {
			downloads++
			t.Logf("  %s", f)
		}
	}
	if downloads == 0 {
		t.Errorf("downloads/ 下没有任何文件被内嵌")
	}
}
