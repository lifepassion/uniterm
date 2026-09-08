//go:build windows
// +build windows

package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WSLFileSession is the file-transfer companion for a WSL terminal. The "remote"
// filesystem is the WSL distribution's own view reached through the Windows UNC
// path `//wsl.localhost/<distro>/...`, so every remote operation is a plain os
// call — no SFTP subsystem, no bundled server, no SSH credentials.
//
// It implements fileTransferSession, so all existing Sftp* bindings and the
// frontend file panel dispatch through it unchanged. Remote paths are kept in
// POSIX form (e.g. /home/user) so the frontend breadcrumb renders normally;
// they are translated to the UNC path at the os boundary.
//
// NOTE: the wsl.localhost share is only reachable with FORWARD slashes on this
// system (backslash UNC returns path-not-found), and `filepath` would rewrite
// them to backslashes, so all UNC path building uses "/" joining.
type WSLFileSession struct {
	baseSession
	distro string
	root   string // //wsl.localhost/<distro> (absolute; no trailing separator)
	cwd    string // remote cwd in POSIX form, e.g. /home/user

	localFSOps // Windows-local pane (ListLocal / Local* / ListLocalDrives)

	mu        sync.RWMutex
	transfers map[string]*TransferTask
	taskSeq   int64

	// uid/gid -> name maps loaded once from /etc/passwd and /etc/group so
	// listings can render real owner/group names instead of numbers.
	mapOnce  sync.Once
	userMap  map[int]string
	groupMap map[int]string

	connectCancel context.CancelFunc
}

// compile-time guarantees that the WSL file session satisfies the Session
// contract. (fileTransferSession is asserted at runtime via getSftp and lives
// in the app package, so it can't be referenced here.)
var _ Session = (*WSLFileSession)(nil)

func NewWSLFileSession(id string) *WSLFileSession {
	return &WSLFileSession{
		baseSession: baseSession{
			id:          id,
			sessionType: "wsl-file",
			status:      StatusDisconnected,
		},
		localFSOps: newLocalFSOps(),
		transfers:  make(map[string]*TransferTask),
	}
}

// Connect resolves the distro (config.Distro, else config.ShellPath wsl://name),
// probes the user home inside the distro and marks the session connected.
func (s *WSLFileSession) Connect(config ConnectionConfig) error {
	distro, _ := parseWSLPath(config.ShellPath)
	if distro == "" {
		s.setStatus(StatusError)
		return fmt.Errorf("empty WSL distribution name")
	}
	s.distro = distro
	s.root = `//wsl.localhost/` + distro
	s.setStatus(StatusConnecting)
	ctx, cancel := context.WithCancel(context.Background())
	s.connectCancel = cancel
	s.cwd = s.resolveHome(ctx, distro)
	s.setStatus(StatusConnected)
	return nil
}

// resolveHome asks the WSL distro for $HOME of its default user, returning a
// POSIX path. Falls back to / (distro root) if the probe fails or the distro is
// not running — the user can still navigate to a real directory afterwards.
func (s *WSLFileSession) resolveHome(ctx context.Context, distro string) string {
	if home, ok := s.probeHome(ctx, distro); ok {
		return home
	}
	return "/"
}

func (s *WSLFileSession) probeHome(ctx context.Context, distro string) (string, bool) {
	cmd := exec.CommandContext(ctx, "wsl.exe", "-d", distro, "--", "sh", "-c", "echo $HOME")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	home := strings.TrimSpace(string(out))
	if home == "" || !strings.HasPrefix(home, "/") {
		return "", false
	}
	return home, true
}

// --- path helpers ----------------------------------------------------------

func (s *WSLFileSession) resolveRemote(p string) string {
	if p == "" {
		return path.Clean(s.cwd)
	}
	if !strings.HasPrefix(p, "/") {
		p = path.Join(s.cwd, p)
	}
	return path.Clean(p)
}

// uncPath translates a POSIX remote path to the absolute forward-slash UNC path
// under the distro root, e.g. /home/user -> //wsl.localhost/Ubuntu/home/user.
func (s *WSLFileSession) uncPath(p string) string {
	rel := strings.Trim(p, "/")
	if rel == "" {
		return s.root
	}
	return s.root + "/" + rel
}

func (s *WSLFileSession) resolveLocal(p string) string {
	if !filepath.IsAbs(p) {
		return filepath.Join(s.localCwd, p)
	}
	return filepath.Clean(p)
}

// joinEntry appends name to a directory. UNC roots stay forward-slashed (see the
// type doc comment); ordinary local paths use filepath.Join.
func joinEntry(dir, name string) string {
	if strings.HasPrefix(dir, `//`) || strings.HasPrefix(dir, `\\`) {
		return dir + "/" + name
	}
	return filepath.Join(dir, name)
}

// fileItemsFromDir builds FileItem entries from an os.ReadDir result.
func fileItemsFromDir(dir string, entries []os.DirEntry) []FileItem {
	files := make([]FileItem, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		var mode os.FileMode
		var modTime time.Time
		var size int64
		if err == nil {
			mode = fi.Mode()
			modTime = fi.ModTime()
			size = fi.Size()
		}
		isDir := e.IsDir()
		full := joinEntry(dir, e.Name())
		if err == nil && mode&os.ModeSymlink != 0 {
			if target, terr := os.Stat(full); terr == nil && target.IsDir() {
				isDir = true
			}
		}
		isHidden := e.Name() != "" && e.Name()[0] == '.'
		if !isHidden {
			isHidden = isPathHidden(full)
		}
		files = append(files, FileItem{
			Name:     e.Name(),
			Size:     size,
			ModTime:  modTime.Format(time.RFC3339),
			Mode:     mode.String(),
			IsDir:    isDir,
			IsHidden: isHidden,
			Owner:    "",
		})
	}
	return files
}

// --- listing ---------------------------------------------------------------

func (s *WSLFileSession) ListRemote(dir string) (FileListResult, error) {
	d := s.resolveRemote(dir)
	entries, err := os.ReadDir(s.uncPath(d))
	if err != nil {
		return FileListResult{}, err
	}
	files := fileItemsFromDir(s.uncPath(d), entries)
	s.applyOwnerGroups(files, d)
	return FileListResult{Files: files, Dir: d}, nil
}

// --- owner/group resolution (matching SFTP's /etc/passwd + /etc/group) -------

func (s *WSLFileSession) ensureNameMaps() {
	s.mapOnce.Do(func() {
		s.userMap = map[int]string{}
		s.groupMap = map[int]string{}
		if b, err := os.ReadFile(s.uncPath("/etc/passwd")); err == nil {
			parseLinuxNameMap(b, s.userMap, 2)
		}
		if b, err := os.ReadFile(s.uncPath("/etc/group")); err == nil {
			parseLinuxNameMap(b, s.groupMap, 2)
		}
	})
}

// applyOwnerGroups fills Owner/Group on the listing. os.Stat through the
// wsl.localhost share yields no POSIX uid/gid, so the per-directory numeric
// uid/gid come from a single `wsl ls -ln` call, then mapped via /etc/passwd +
// /etc/group. Failures degrade to empty owner/group rather than aborting the
// listing.
func (s *WSLFileSession) applyOwnerGroups(files []FileItem, dir string) {
	s.ensureNameMaps()
	if len(files) == 0 {
		return
	}
	out, err := exec.Command("wsl.exe", "-d", s.distro, "--", "ls", "-lAn", dir).Output()
	if err != nil {
		return
	}
	if len(files) == 0 {
		return
	}
	m := make(map[string][2]int, len(files))
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		uid, err1 := strconv.Atoi(fields[2])
		gid, err2 := strconv.Atoi(fields[3])
		if err1 != nil || err2 != nil {
			continue
		}
		m[strings.Join(fields[8:], " ")] = [2]int{uid, gid}
	}
	for i := range files {
		og, ok := m[files[i].Name]
		if !ok {
			continue
		}
		files[i].Owner = s.nameForID(s.userMap, og[0])
		files[i].Group = s.nameForID(s.groupMap, og[1])
	}
}

func (s *WSLFileSession) nameForID(m map[int]string, id int) string {
	if n, ok := m[id]; ok {
		return n
	}
	return fmt.Sprintf("%d", id)
}

func (s *WSLFileSession) ChangeRemoteDir(dir string) (FileListResult, error) {
	d := s.resolveRemote(dir)
	fi, err := os.Stat(s.uncPath(d))
	if err != nil {
		// Windows-drive drvfs mounts (/mnt/<X>) aren't reachable through the
		// wsl.localhost share — Windows denies the drive→WSL→drive loopback
		// (ERROR_ACCESS_DENIED). Those files are the same as the local drive,
		// so guide the user to the local pane instead of failing silently.
		if os.IsPermission(err) && strings.HasPrefix(d, "/mnt/") {
			return FileListResult{}, fmt.Errorf("cannot open %s: /mnt Windows-drive mounts aren't reachable through the WSL file share (open %s from the local pane instead)", d, s.wslMountDrive(d))
		}
		return FileListResult{}, err
	}
	if !fi.IsDir() {
		return FileListResult{}, fmt.Errorf("not a directory: %s", d)
	}
	s.cwd = d
	return s.ListRemote(d)
}

// wslMountDrive maps a /mnt/<letter> mount path back to its Windows drive
// (e.g. /mnt/c -> C:\) for the error hint above.
func (s *WSLFileSession) wslMountDrive(d string) string {
	rest := strings.TrimPrefix(d, "/mnt/")
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	if len(rest) == 1 {
		return strings.ToUpper(rest) + `:\`
	}
	return d
}

// The Windows-local pane (ListLocal / ChangeLocalDir / ListLocalDrives /
// LocalRemove / LocalRename / LocalMkdir / LocalGetContent / LocalPutContent /
// LocalCopy / LocalMove) is provided by the embedded localFSOps via method
// promotion, exactly like SMB/FTP/WebDAV/S3/SCP — nothing to delegate here.

// --- remote attributes / dirs ----------------------------------------------

func (s *WSLFileSession) MakeDir(dir string) error {
	return os.Mkdir(s.uncPath(s.resolveRemote(dir)), 0o755)
}

func (s *WSLFileSession) Remove(p string, recursive bool) error {
	c := s.resolveRemote(p)
	if c == "/" || c == "." {
		return fmt.Errorf("refusing to delete path: %s", c)
	}
	full := s.uncPath(c)
	if recursive {
		return os.RemoveAll(full)
	}
	return os.Remove(full)
}

func (s *WSLFileSession) Rename(oldName, newName string) error {
	return os.Rename(s.uncPath(s.resolveRemote(oldName)), s.uncPath(s.resolveRemote(newName)))
}

func (s *WSLFileSession) Chmod(p string, mode os.FileMode) error {
	return os.Chmod(s.uncPath(s.resolveRemote(p)), mode)
}

// --- content read/write ------------------------------------------------------

func (s *WSLFileSession) GetContent(remotePath string) ([]byte, error) {
	return os.ReadFile(s.uncPath(s.resolveRemote(remotePath)))
}

func (s *WSLFileSession) PutContent(remotePath string, content []byte) error {
	return os.WriteFile(s.uncPath(s.resolveRemote(remotePath)), content, 0o644)
}

func (s *WSLFileSession) Copy(oldPath, newPath string) error {
	return copyPath(s.uncPath(s.resolveRemote(oldPath)), s.uncPath(s.resolveRemote(newPath)), nil)
}

func (s *WSLFileSession) Move(oldPath, newPath string) error {
	oldU := s.uncPath(s.resolveRemote(oldPath))
	newU := s.uncPath(s.resolveRemote(newPath))
	if err := os.Rename(oldU, newU); err == nil {
		return nil
	}
	if err := copyPath(oldU, newU, nil); err != nil {
		return err
	}
	return os.RemoveAll(oldU)
}

// --- transfers ---------------------------------------------------------------

func (s *WSLFileSession) Get(remotePath, localPath string, recursive bool) (string, error) {
	return s.startLocalTransfer("download", s.resolveLocal(localPath), s.uncPath(s.resolveRemote(remotePath)))
}

func (s *WSLFileSession) Put(localPath, remotePath string, recursive bool) (string, error) {
	return s.startLocalTransfer("upload", s.resolveLocal(localPath), s.uncPath(s.resolveRemote(remotePath)))
}

// startLocalTransfer copies a file or tree between the Windows-local pane and
// the WSL UNC path. Both endpoints are on the same machine, so this is a local
// copy; a Task is reported through the usual OSC 633 transfer events so the
// frontend TransferPanel stays in sync.
func (s *WSLFileSession) startLocalTransfer(tfType, local, remote string) (string, error) {
	src := remote
	dst := local
	if tfType == "upload" {
		src = local
		dst = remote
	}
	total, err := dirSize(src)
	if err != nil {
		return "", err
	}
	prefix := "ul"
	if tfType == "download" {
		prefix = "dl"
	}
	task := &TransferTask{
		ID:         s.nextTaskID(prefix),
		Type:       tfType,
		LocalPath:  local,
		RemotePath: remote,
		Total:      total,
		Status:     "running",
	}
	task.start()
	s.mu.Lock()
	s.transfers[task.ID] = task
	s.mu.Unlock()
	s.emitTransferStart(task)
	go func() {
		defer func() {
			task.done()
			s.mu.Lock()
			delete(s.transfers, task.ID)
			s.mu.Unlock()
		}()
		if err := copyPath(src, dst, task); err != nil {
			task.Status = "error"
			s.emitTransferEvent(task, err)
			return
		}
		task.Progress = task.Total
		task.Status = "done"
		s.emitTransferProgress(task)
		s.emitTransferComplete(task)
	}()
	return task.ID, nil
}

// dirSize sums the bytes of a file or all files under a directory tree.
func dirSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if !fi.IsDir() {
		return fi.Size(), nil
	}
	var total int64
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if ei, err := e.Info(); err == nil && !ei.IsDir() {
				total += ei.Size()
				continue
			}
			full := joinEntry(dir, e.Name())
			if fi2, err := os.Stat(full); err == nil && fi2.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return total, walk(p)
}

// copyPath copies a file or directory tree. task, if non-nil, receives byte
// progress updates and pauses/cancels via transferTask.
func copyPath(src, dst string, task *TransferTask) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyPath(joinEntry(src, e.Name()), joinEntry(dst, e.Name()), task); err != nil {
				return err
			}
		}
		return nil
	}
	return copyFile(src, dst, task)
}

func copyFile(src, dst string, task *TransferTask) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if task != nil {
		task.waitIfPaused() // blocks while paused / returns on cancel
	}
	buf := make([]byte, 64*1024)
	for {
		task.waitIfPaused()
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			if task != nil {
				task.Progress += int64(n)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	return out.Close()
}

func (s *WSLFileSession) CancelTransfer(taskID string) error {
	s.mu.Lock()
	t, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (s *WSLFileSession) PauseTransfer(taskID string) error {
	s.mu.Lock()
	t, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	t.paused = true
	t.Status = "paused"
	s.emitTransferComplete(t)
	return nil
}

func (s *WSLFileSession) ResumeTransfer(taskID string) error {
	s.mu.Lock()
	t, ok := s.transfers[taskID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	t.paused = false
	t.Status = "running"
	close(t.pauseCh)
	t.pauseCh = make(chan struct{})
	s.emitTransferStart(t)
	return nil
}

func (s *WSLFileSession) nextTaskID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&s.taskSeq, 1))
}

// --- Session interface --------------------------------------------------------

func (s *WSLFileSession) Write(data []byte) error {
	return fmt.Errorf("not a terminal session")
}

func (s *WSLFileSession) Resize(_, _ int) error { return nil }

func (s *WSLFileSession) IsConnected() bool {
	return s.Status() == StatusConnected
}

func (s *WSLFileSession) Disconnect() error {
	if s.connectCancel != nil {
		s.connectCancel()
	}
	s.mu.Lock()
	for _, t := range s.transfers {
		if t.cancel != nil {
			t.cancel()
		}
	}
	s.mu.Unlock()
	s.setStatus(StatusDisconnected)
	return nil
}

// --- transfer event emission (OSC 633 window-reporting, like SFTP) -----------

func (s *WSLFileSession) emitTransferStart(task *TransferTask) {
	name := path.Base(task.RemotePath)
	if task.Type == "download" {
		name = path.Base(task.RemotePath)
	}
	payload := map[string]interface{}{
		"type": "sftp:transfer", "taskId": task.ID, "event": "start",
		"tfType": task.Type, "name": name, "total": task.Total,
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}

func (s *WSLFileSession) emitTransferProgress(task *TransferTask) {
	payload := map[string]interface{}{
		"type": "sftp:transfer", "taskId": task.ID, "event": "progress",
		"progress": task.Progress, "total": task.Total,
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}

func (s *WSLFileSession) emitTransferComplete(task *TransferTask) {
	payload := map[string]interface{}{
		"type": "sftp:transfer", "taskId": task.ID, "event": "complete", "status": task.Status,
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}

func (s *WSLFileSession) emitTransferEvent(task *TransferTask, err error) {
	payload := map[string]interface{}{
		"type": "sftp:transfer", "taskId": task.ID, "event": "complete",
		"status": "error", "error": err.Error(),
	}
	jsonBytes, _ := json.Marshal(payload)
	s.emitData([]byte("\x1b]633;S" + string(jsonBytes) + "\x07"))
}
