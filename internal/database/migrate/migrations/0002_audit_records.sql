-- 審計記錄存儲（規格 §25.1 Activity Audit、§25.2 Root Audit、§25.3 不得物理刪除）。
--
-- 表結構約定（決策記錄 DEC-011）：一般表（非 STRICT）；時間戳一律 INTEGER，Unix 毫秒 UTC。
-- 標識約定（決策記錄 DEC-014；本表是其第一個帶業務主鍵的表，此處定案）：
--   主鍵與實體標識一律以 TEXT 存 36 字元小寫正規 UUIDv7 字串——與 JSON 承載、
--   日誌裡的 request_id 以及 API 回應同形，人工用 sqlite 工具排錯時可以直接拿去比對；
--   BLOB 省下的位元組換不掉這個一致性。
-- 為什麼是兩張表而不是一張加 scope 欄位：普通日誌可輪替可淘汰、審計不可（規格 §32），
--   Root 審計與活動審計的可讀範圍也不同（後續步驟要把兩類查詢的授權隔開）。
--   分表讓「跨作用域讀到」需要顯式改查詢目標，而不是忘記加一個 WHERE。
-- 只追加（AUD-003、§25.3）：觸發器把 UPDATE／DELETE 擋在資料庫層，擋的是「有人透過 SQL 改它」——
--   握有資料庫檔案與作業系統權限的人仍然可以改檔，這條邊界與 ER-SEC-001 §1
--   「規格承認不可防禦完全惡意 Root」一致，不假裝是對抗性的。
-- 活動參照：目前 activities 表尚不存在，因此只校驗標識形狀、不宣告外鍵
--   （指向不存在的表會讓寫入直接失敗）；該表落地時補 FOREIGN KEY。
-- 變更摘要（changes_json）：由 internal/audit 序列化並先過 ER-SEC-001 §7 的遮罩管線，
--   值為字串時不會帶著 PIN、憑證或聊天正文進表；這裡加 json_valid 只是把「存進去的是壞 JSON」
--   這種自傷擋在資料庫層。

CREATE TABLE root_audit (
    id           TEXT PRIMARY KEY CHECK (length(id) = 36),
    created_at   INTEGER NOT NULL CHECK (created_at > 0),
    actor_kind   TEXT NOT NULL CHECK (length(actor_kind) BETWEEN 1 AND 32),
    actor_id     TEXT CHECK (actor_id IS NULL OR length(actor_id) = 36),
    action       TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    target_kind  TEXT NOT NULL CHECK (length(target_kind) BETWEEN 1 AND 32),
    target_id    TEXT CHECK (target_id IS NULL OR length(target_id) BETWEEN 1 AND 64),
    -- 原因：Root 登入這類事件本身沒有可寫的原因，因此可空；
    -- 高風險操作「原因必填」由 internal/audit 的記錄型別把關（見該套件註解）。
    reason       TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
    -- 關聯 ID：只在由請求驅動的操作時存在；啟動、背景任務與一次性命令為 NULL，
    -- 不拿空字串或假值冒充（NULL＝本來就沒有請求）。
    request_id   TEXT CHECK (request_id IS NULL OR length(request_id) BETWEEN 1 AND 64),
    changes_json TEXT NOT NULL CHECK (changes_json <> '' AND json_valid(changes_json))
    -- Root 表刻意沒有 activity_id：Root 事件不屬於任何活動，寫錯時連欄位都放不進去，
    -- 比「存一個 NULL 等著被誤讀」誠實。
);

CREATE TABLE activity_audit (
    id           TEXT PRIMARY KEY CHECK (length(id) = 36),
    created_at   INTEGER NOT NULL CHECK (created_at > 0),
    activity_id  TEXT NOT NULL CHECK (length(activity_id) = 36),
    actor_kind   TEXT NOT NULL CHECK (length(actor_kind) BETWEEN 1 AND 32),
    actor_id     TEXT CHECK (actor_id IS NULL OR length(actor_id) = 36),
    action       TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    target_kind  TEXT NOT NULL CHECK (length(target_kind) BETWEEN 1 AND 32),
    target_id    TEXT CHECK (target_id IS NULL OR length(target_id) BETWEEN 1 AND 64),
    reason       TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
    request_id   TEXT CHECK (request_id IS NULL OR length(request_id) BETWEEN 1 AND 64),
    changes_json TEXT NOT NULL CHECK (changes_json <> '' AND json_valid(changes_json))
);

-- 查詢索引：審計的回看是「某活動/某對象最近被做過什麼」，一律走時間倒序 + 標識破平，
-- 與 internal/audit 的鍵集游標分頁同一個順序（DEC-013：深度分頁要穩定）。
CREATE INDEX root_audit_time_idx ON root_audit (created_at DESC, id DESC);
CREATE INDEX root_audit_target_idx ON root_audit (target_kind, target_id, created_at DESC, id DESC);
CREATE INDEX activity_audit_activity_time_idx ON activity_audit (activity_id, created_at DESC, id DESC);
CREATE INDEX activity_audit_target_idx ON activity_audit (target_kind, target_id, created_at DESC, id DESC);

CREATE TRIGGER root_audit_no_update
    BEFORE UPDATE ON root_audit
BEGIN
    SELECT RAISE(ABORT, 'root_audit 是只追加存儲：不得修改既有記錄（規格 §25.3、AUD-003）');
END;

CREATE TRIGGER root_audit_no_delete
    BEFORE DELETE ON root_audit
BEGIN
    SELECT RAISE(ABORT, 'root_audit 是只追加存儲：不得刪除既有記錄（規格 §25.3、AUD-003）');
END;

CREATE TRIGGER activity_audit_no_update
    BEFORE UPDATE ON activity_audit
BEGIN
    SELECT RAISE(ABORT, 'activity_audit 是只追加存儲：不得修改既有記錄（規格 §25.3、AUD-003）');
END;

CREATE TRIGGER activity_audit_no_delete
    BEFORE DELETE ON activity_audit
BEGIN
    SELECT RAISE(ABORT, 'activity_audit 是只追加存儲：不得刪除既有記錄（規格 §25.3、AUD-003）');
END;
