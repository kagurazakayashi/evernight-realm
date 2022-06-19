"""迁移中断、异常重启与数据目录权限错误的进程级验证。

本脚本只操作自己建立的隔离数据目录（evernight-data/step045-run/），不接触真实活动数据，
也不修改任何产品代码；它用三种手段制造故障：

  A 迁移执行中强杀：由外部 sqlite 连接持有写锁（BEGIN IMMEDIATE 不提交），
    并把该目录的 busy_timeout_ms 调大，使服务停在「建立版本表」这一步；
    在此窗口内 taskkill /F 强杀，检查是否留下半迁移，再重跑验证可自愈。
  B 服务运行中强杀（模拟断电）：写入一笔已提交但未 checkpoint 的資料後強杀进程，
    重启后验证 WAL 恢复、数据不丢、锁档不阻塞重启。
  C 数据目录权限错误：資料庫档唯读（attrib +r）、data_dir 指向已存在的普通档案、
    目录拒绝写入（icacls /deny，需权限，不足时记 SKIP）。

三类都附带同一判据：失败时错误讯息可诊断（含路径或原因），且不会自动删除资料档。

用法：
  python verify_crash_recovery.py            # 全部场景
  python verify_crash_recovery.py A B C1     # 只跑指定场景（前缀匹配）

前提：先构建执行档
  go build -o evernight-data/step045-build/evernight-server.exe ./cmd/evernight-server
"""

import hashlib
import json
import os
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(HERE)))
EXE = os.path.join(REPO, "evernight-data", "step045-build", "evernight-server.exe")
WORK = os.path.join(REPO, "evernight-data", "step045-run")

CREATE_NEW_PROCESS_GROUP = 0x00000200
PORT_BASE = 5231

results = []


# --------------------------------------------------------------------------
# 基础工具
# --------------------------------------------------------------------------

def log(msg):
    print(msg, flush=True)


def check(name, ok, detail="", skipped=False):
    tag = "SKIP" if skipped else ("PASS" if ok else "FAIL")
    results.append((name, ok, skipped))
    line = "    [%s] %s" % (tag, name)
    if detail:
        line += "  " + detail.replace("\n", " / ")
    log(line)


def sha256(path):
    if not os.path.exists(path):
        return None
    h = hashlib.sha256()
    try:
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(65536), b""):
                h.update(chunk)
    except OSError as exc:
        # 權限被拒時不讓驗證腳本自己崩潰：以可讀性的事實取代摘要。
        return "unreadable:%s" % exc.__class__.__name__
    return h.hexdigest()


def case_dir(name):
    path = os.path.join(WORK, name)
    if os.path.isdir(path):
        shutil.rmtree(path)
    os.makedirs(path)
    return path


def write_config(dirpath, port=None, busy_timeout_ms=5000):
    lines = ["database:", "  path: \"evernight.db\"", "  busy_timeout_ms: %d" % busy_timeout_ms]
    if port is not None:
        lines = ["server:", '  listen: "127.0.0.1:%d"' % port] + lines
    with open(os.path.join(dirpath, "config.yaml"), "w", encoding="utf-8") as f:
        f.write("# 驗證用組態（隔離資料目錄）\n" + "\n".join(lines) + "\n")


def run_cli(*args, timeout=90):
    """執行 migrate 等子命令，回傳 (退出碼, 輸出)。"""
    proc = subprocess.run(
        [EXE] + list(args) + ["--data-dir", CASE.current],
        capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=timeout,
    )
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def launch_server(port):
    """只啟動服務並等一小段寬限期（用於「預期它卡住」的場景）。"""
    proc = subprocess.Popen(
        [EXE, "--data-dir", CASE.current],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, encoding="utf-8", errors="replace",
        creationflags=CREATE_NEW_PROCESS_GROUP,
    )
    time.sleep(3.0)
    return proc


def start_server(port, extra_args=()):
    """啟動服務並等待端口可連，回傳 Popen。"""
    proc = subprocess.Popen(
        [EXE, "--data-dir", CASE.current] + list(extra_args),
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, encoding="utf-8", errors="replace",
        creationflags=CREATE_NEW_PROCESS_GROUP,
    )
    deadline = time.time() + 30
    while time.time() < deadline:
        if proc.poll() is not None:
            return proc
        try:
            socket.create_connection(("127.0.0.1", port), timeout=0.3).close()
            return proc
        except OSError:
            time.sleep(0.15)
    return proc


def force_kill(proc):
    """強殺（模擬斷電）：不給進程任何清理機會。"""
    subprocess.run(["taskkill", "/F", "/PID", str(proc.pid)],
                   capture_output=True, text=True, errors="replace")
    try:
        proc.wait(timeout=20)
    except Exception:
        pass


def graceful_stop(proc):
    proc.send_signal(signal.CTRL_BREAK_EVENT)
    out = proc.stdout.read()
    return proc.wait(timeout=40), out


def hold_write_lock(db_path, busy_timeout_ms=100):
    """用外部連線持有寫鎖（不是本服務的執行檔，故不受單寫入實例鎖約束）。"""
    conn = sqlite3.connect(db_path, isolation_level=None)
    conn.execute("PRAGMA busy_timeout=%d" % busy_timeout_ms)
    conn.execute("BEGIN IMMEDIATE")
    return conn


def prepare_empty_wal_db(db_path):
    """建立「WAL 模式、零資料表」的庫（檔頭 page_count=1，application_id 未標記）。"""
    conn = sqlite3.connect(db_path)
    conn.execute("PRAGMA journal_mode(WAL)")
    conn.commit()
    conn.close()


def db_tables(db_path):
    conn = sqlite3.connect("file:%s?mode=ro" % db_path.replace("\\", "/"), uri=True)
    try:
        rows = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
        ).fetchall()
        return [r[0] for r in rows]
    finally:
        conn.close()


def db_user_version(db_path):
    """讀檔頭 user_version（純檔案讀，不驚動 SQLite 恢復機制）。"""
    with open(db_path, "rb") as f:
        head = f.read(100)
    return int.from_bytes(head[60:64], "big") if len(head) >= 64 else None


def db_application_id(db_path):
    with open(db_path, "rb") as f:
        head = f.read(100)
    return int.from_bytes(head[68:72], "big") if len(head) >= 72 else None


def integrity_ok(db_path):
    conn = sqlite3.connect(db_path)
    try:
        ok = conn.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        fk = conn.execute("PRAGMA foreign_key_check").fetchall()
        return ok and not fk
    finally:
        conn.close()


def version_rows(db_path):
    conn = sqlite3.connect("file:%s?mode=ro" % db_path.replace("\\", "/"), uri=True)
    try:
        return conn.execute("SELECT version, name FROM schema_migrations ORDER BY version").fetchall()
    except sqlite3.OperationalError:
        return None
    finally:
        conn.close()


def http_get(url):
    try:
        with urllib.request.urlopen(url, timeout=15) as resp:
            return resp.status, resp.read().decode("utf-8")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode("utf-8")


class _Case:
    current = None


CASE = _Case()


# --------------------------------------------------------------------------
# 場景 A：遷移執行中被強殺
# --------------------------------------------------------------------------

def scenario_a_kill_during_migration(busy_timeout_ms=30000):
    global CASE
    log("\n=== A 遷移執行中強殺（程序級中斷）")

    # A0 基準：沒有外部鎖時，同一結構的 migrate 應該很快完成。
    CASE.current = case_dir("a0-baseline")
    db0 = os.path.join(CASE.current, "evernight.db")
    write_config(CASE.current, busy_timeout_ms=busy_timeout_ms)
    prepare_empty_wal_db(db0)
    t0 = time.time()
    code, out = run_cli("migrate")
    base_dt = time.time() - t0
    check("A0 基準：無鎖 migrate 成功且秒級完成", code == 0 and base_dt < 5,
          "退出碼 %s，耗時 %.2fs" % (code, base_dt))
    check("A0 基準：版本表已建立 version=1",
          (version_rows(db0) or []) == [(1, "server_settings")], str(version_rows(db0)))

    # A1 外部持寫鎖：migrate 停在寫入階段（不在毫秒級完成），作為強殺窗口。
    CASE.current = case_dir("a1-killed")
    db1 = os.path.join(CASE.current, "evernight.db")
    write_config(CASE.current, busy_timeout_ms=busy_timeout_ms)
    prepare_empty_wal_db(db1)
    holder = hold_write_lock(db1)
    proc = subprocess.Popen(
        [EXE, "migrate", "--data-dir", CASE.current],
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, encoding="utf-8", errors="replace",
        creationflags=CREATE_NEW_PROCESS_GROUP,
    )
    time.sleep(4.0)
    stuck = proc.poll() is None
    check("A1 外部持鎖時 migrate 仍卡在遷移階段（窗口成立）", stuck,
          "4 秒後進程狀態 %s" % ("仍在執行" if stuck else "已結束"))
    digest_before = sha256(db1)
    if stuck:
        force_kill(proc)
    else:
        proc.wait(timeout=busy_timeout_ms / 1000.0 + 30)
        out = proc.stdout.read()
        log("      migrate 未卡住的輸出：%s" % out.strip().replace("\n", " / "))
    check("A1 強殺後進程已結束（非正常退出）", proc.poll() is not None,
          "退出碼 %s" % proc.returncode)

    # A2 中斷後的中間態檢查：不得出現半套用的結構或版本記錄。
    holder.execute("ROLLBACK")
    holder.close()
    tables = db_tables(db1)
    check("A2 中斷後資料庫檔仍在（未被自動刪除）", os.path.exists(db1), str(digest_before)[:16])
    check("A2 中斷後無半迁移：未留下任何資料表", tables == [], "實際資料表 %s" % tables)
    check("A2 中斷後檔頭 user_version 仍為 0", db_user_version(db1) == 0, str(db_user_version(db1)))
    check("A2 中斷後完整性檢查通過", integrity_ok(db1))

    # A3 重跑自愈：同一目錄再次 migrate 應完整套用並落到 version=1。
    code, out = run_cli("migrate")
    check("A3 中斷後重跑 migrate 成功", code == 0, out.strip().splitlines()[-1] if out.strip() else "")
    check("A3 重跑後版本記錄與結構齊全",
          (version_rows(db1) or []) == [(1, "server_settings")] and "server_settings" in db_tables(db1),
          "%s / %s" % (version_rows(db1), db_tables(db1)))
    check("A3 重跑後檔頭標記為 1 且完整性通過",
          db_user_version(db1) == 1 and integrity_ok(db1), "user_version=%s" % db_user_version(db1))

    # A4 服務方式（run）在遷移階段被強殺後，重啟服務應能完成並對外就緒。
    CASE.current = case_dir("a4-server-killed")
    db4 = os.path.join(CASE.current, "evernight.db")
    port = PORT_BASE + 4
    write_config(CASE.current, port=port, busy_timeout_ms=busy_timeout_ms)
    prepare_empty_wal_db(db4)
    holder = hold_write_lock(db4)
    srv = launch_server(port)
    check("A4 啟動時卡在遷移（進程仍在）", srv.poll() is None,
          "3 秒後進程狀態 %s" % ("仍在執行" if srv.poll() is None else "已結束"))
    check("A4 卡在遷移期間不對外開放監聽（不會半就緒服務）", not _port_open(port))
    force_kill(srv)
    holder.execute("ROLLBACK")
    holder.close()
    check("A4 中斷後無半迁移（無資料表）", db_tables(db4) == [], str(db_tables(db4)))
    srv = start_server(port)
    status, body = http_get("http://127.0.0.1:%d/ready" % port) if _port_open(port) else (-1, "")
    check("A4 重啟後服務就緒", status == 200, "GET /ready → %s %s" % (status, body.strip()))
    if srv.poll() is None:
        code, out = graceful_stop(srv)
        check("A4 重啟後的服務可正常優雅停止", code == 0, "退出碼 %s" % code)
    check("A4 自愈後版本為 1", (version_rows(db4) or []) == [(1, "server_settings")],
          str(version_rows(db4)))


def _port_open(port):
    try:
        socket.create_connection(("127.0.0.1", port), timeout=0.5).close()
        return True
    except OSError:
        return False


# --------------------------------------------------------------------------
# 場景 B：服務運行中強殺（模擬斷電）與 WAL 恢復
# --------------------------------------------------------------------------

def scenario_b_crash_recovery():
    global CASE
    log("\n=== B 服務運行中強殺（模擬斷電）與 WAL 恢復")
    CASE.current = case_dir("b-crash")
    db = os.path.join(CASE.current, "evernight.db")
    port = PORT_BASE + 1
    write_config(CASE.current, port=port)

    srv = start_server(port)
    status, body = http_get("http://127.0.0.1:%d/ready" % port)
    check("B0 啟動後就緒", status == 200, "GET /ready → %s" % status)

    # 寫入一筆已提交但尚未 checkpoint 的資料（服務本身尚無業務寫入端點）。
    conn = sqlite3.connect(db)
    conn.execute("PRAGMA busy_timeout=5000")
    conn.execute("INSERT INTO server_settings (key, value, updated_at) VALUES ('crash_probe','1',0)")
    conn.commit()
    wal = os.path.join(CASE.current, "evernight.db-wal")
    wal_bytes = os.path.getsize(wal) if os.path.exists(wal) else 0
    check("B1 已提交資料留在 WAL 中（尚未 checkpoint）", wal_bytes > 0, "-wal %d 位元組" % wal_bytes)
    conn.close()

    digest = sha256(db)
    force_kill(srv)
    check("B2 強殺後進程結束（未走優雅停止）", srv.poll() is not None, "退出碼 %s" % srv.returncode)
    check("B2 強殺後資料庫檔與 WAL 均仍在（不自動刪除）",
          os.path.exists(db) and os.path.exists(wal) and sha256(db) == digest,
          "-wal %s 位元組" % (os.path.getsize(wal) if os.path.exists(wal) else -1))

    srv = start_server(port)
    status, body = http_get("http://127.0.0.1:%d/ready" % port)
    check("B3 重啟成功且就緒（殘留鎖檔不阻塞重啟）", status == 200, "GET /ready → %s %s" % (status, body.strip()))
    conn = sqlite3.connect("file:%s?mode=ro" % db.replace("\\", "/"), uri=True)
    try:
        got = conn.execute("SELECT value FROM server_settings WHERE key='crash_probe'").fetchall()
    finally:
        conn.close()
    check("B4 已提交資料在重啟後仍可讀回（WAL 回復成功）", got == [("1",)], str(got))
    check("B4 重啟後完整性與外鍵檢查通過", integrity_ok(db))
    code, out = graceful_stop(srv)
    check("B5 之後仍可正常優雅停止", code == 0, "退出碼 %s" % code)


# --------------------------------------------------------------------------
# 場景 C：資料目錄／資料庫檔權限錯誤
# --------------------------------------------------------------------------

def scenario_c_readonly_db():
    """資料庫檔唯讀：需要寫入的路徑必須明確失敗、可診斷、且不刪資料。"""
    global CASE
    log("\n=== C1 資料庫檔唯讀（attrib +r）")
    CASE.current = case_dir("c1-readonly")
    db = os.path.join(CASE.current, "evernight.db")
    port = PORT_BASE + 2
    write_config(CASE.current, port=port)
    # 只在「尚未遷移」的庫上測試：已完成遷移的庫本來就不需要寫入，唯讀也會成功。
    prepare_empty_wal_db(db)
    subprocess.run(["attrib", "+r", db], check=False, capture_output=True)
    if not _is_readonly(db):
        check("C1 跳過：本機無法將資料庫檔設為唯讀", False, skipped=True)
        return
    digest = sha256(db)

    code, out = run_cli("migrate")
    last = out.strip().splitlines()[-1] if out.strip() else ""
    check("C1 唯讀庫上需要寫入的遷移失敗（退出碼非 0）", code != 0, "退出碼 %s" % code)
    check("C1 錯誤訊息可診斷（含路徑、唯讀或權限原因）",
          db in _norm(out) or "read-only" in out.lower() or "readonly" in out.lower()
          or "permission" in out.lower() or "denied" in out.lower() or "唯讀" in out or "拒絕" in out,
          last)
    check("C1 失敗後資料庫檔仍在且內容未變",
          os.path.exists(db) and sha256(db) == digest, str(digest)[:16])

    # 同一唯讀庫改以服務方式啟動：應失敗、不開放監聽、不刪資料。
    srv = launch_server(port)
    code = srv.wait(timeout=120)
    check("C1 唯讀庫啟動服務亦失敗（不帶病對外服務）",
          code != 0 and not _port_open(port), "退出碼 %s，端口可連=%s" % (code, _port_open(port)))
    check("C1 啟動失敗後資料檔仍在", os.path.exists(db) and sha256(db) == digest)

    subprocess.run(["attrib", "-r", db], check=False, capture_output=True)
    code, out = run_cli("migrate")
    check("C1 解除唯讀後遷移可完成且資料完好",
          code == 0 and (version_rows(db) or []) == [(1, "server_settings")] and integrity_ok(db),
          "退出碼 %s" % code)


def scenario_c_data_dir_is_file():
    """資料目錄指向已存在的普通檔案：建立目錄失敗要點出路徑。"""
    global CASE
    log("\n=== C2 data_dir 指向已存在的普通檔案")
    parent = case_dir("c2-dirfile")
    blocker = os.path.join(parent, "not-a-dir")
    with open(blocker, "w", encoding="utf-8") as f:
        f.write("這是一個檔案，不是目錄\n")
    digest = sha256(blocker)
    CASE.current = blocker
    code, out = run_cli("migrate")
    check("C2 指向檔案的 data_dir 啟動失敗", code != 0, "退出碼 %s" % code)
    check("C2 錯誤訊息指出該路徑", blocker in _norm(out) or "not-a-dir" in out,
          out.strip().splitlines()[-1] if out.strip() else "")
    check("C2 該檔案未被刪除且內容未變", os.path.exists(blocker) and sha256(blocker) == digest)


def scenario_c_deny_directory():
    """整個資料目錄被拒絕寫入：啟動必須失敗並給出可診斷訊息。"""
    global CASE
    log("\n=== C3 資料目錄拒絕寫入（icacls /deny）")
    CASE.current = case_dir("c3-denied")
    user = "%s\\%s" % (os.environ.get("COMPUTERNAME", "."), os.environ.get("USERNAME", ""))
    # 對「尚未建立任何資料」的目錄先拒絕寫入：服務必須在建立子目錄／鎖檔／開庫時明確失敗。
    acl = subprocess.run(["icacls", CASE.current, "/deny", "%s:(OI)(CI)(W)" % user],
                         capture_output=True, text=True, errors="replace")
    if acl.returncode != 0:
        check("C3 跳過：icacls /deny 需要額外權限（本機不可用）", False,
              (acl.stdout + acl.stderr).strip(), skipped=True)
        return
    try:
        code, out = run_cli("migrate")
        lines = out.strip().splitlines()
        check("C3 目錄不可寫時啟動失敗（退出碼非 0）", code != 0, "退出碼 %s" % code)
        check("C3 錯誤訊息可診斷（含目錄路徑或寫入被拒原因）",
              CASE.current in _norm(out) or "access" in out.lower() or "denied" in out.lower()
              or "permission" in out.lower() or "拒絕" in out or "許可" in out or "失敗" in out,
              lines[-1] if lines else "")
        check("C3 失敗後資料目錄仍存在（不自動刪除）", os.path.isdir(CASE.current))
    finally:
        subprocess.run(["icacls", CASE.current, "/remove:d", user],
                       capture_output=True, text=True, errors="replace")
        write_config(CASE.current)
        code, out = run_cli("migrate")
        db = os.path.join(CASE.current, "evernight.db")
        check("C3 恢復寫入權限後可正常建立且完整性通過",
              code == 0 and os.path.exists(db) and integrity_ok(db), "退出碼 %s" % code)


def _is_readonly(path):
    import ctypes
    attr = ctypes.windll.kernel32.GetFileAttributesW(str(path))
    return bool(attr != -1 and attr & 0x1)


def _norm(text):
    return text.replace("\\", "/")


# --------------------------------------------------------------------------
# 入口
# --------------------------------------------------------------------------

SCENARIOS = {
    "A": ("遷移執行中強殺", scenario_a_kill_during_migration),
    "B": ("異常重啟與 WAL 恢復", scenario_b_crash_recovery),
    "C1": ("資料庫檔唯讀", scenario_c_readonly_db),
    "C2": ("data_dir 指向檔案", scenario_c_data_dir_is_file),
    "C3": ("目錄拒絕寫入", scenario_c_deny_directory),
}


def main(argv):
    if not os.path.isfile(EXE):
        raise SystemExit("找不到執行檔：%s\n請先執行：go build -o evernight-data/step045-build/evernight-server.exe ./cmd/evernight-server" % EXE)
    os.makedirs(WORK, exist_ok=True)

    wanted = argv or list(SCENARIOS)
    for key in sorted(SCENARIOS, key=lambda k: (len(k), k)):
        if not any(key.startswith(w.upper()) or w.upper().startswith(key) for w in wanted):
            continue
        title, fn = SCENARIOS[key]
        try:
            fn()
        except Exception as exc:  # 單一場景異常不掩蓋其他場景
            check("%s 場景執行異常" % key, False, "%s: %s" % (type(exc).__name__, exc))
            log("      traceback: %s" % __import__("traceback").format_exc(limit=3).strip())

    total = len(results)
    passed = sum(1 for _, ok, sk in results if ok and not sk)
    skipped = sum(1 for _, _, sk in results if sk)
    log("\n結果：%d/%d PASS，%d SKIP（共 %d 項）" % (passed, total - skipped, skipped, total))
    return 0 if passed == total - skipped else 1


if __name__ == "__main__":
    sys.exit(main([a for a in sys.argv[1:]]))
