-- 伺服器層鍵值設定（規格 §27：伺服器配置與版本）。
--
-- 表結構約定（決策記錄 DEC-011）：一般表（非 STRICT）；
-- 時間戳一律 INTEGER，Unix 毫秒 UTC，不使用本地時間字串。
CREATE TABLE server_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
