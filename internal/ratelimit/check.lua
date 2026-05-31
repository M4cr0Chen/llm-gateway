-- check.lua: atomic per-request rate-limit check for RPM and TPM.
--
-- Prunes both the RPM and TPM sliding windows, evaluates RPM and (if RPM
-- passes) TPM, and only adds an RPM tick when both checks pass — so a
-- TPM-rejected request does NOT consume an RPM slot.
--
-- TPM members are encoded as "<request_id>:<tokens>" by tpm_record.lua;
-- this script parses the ":N" suffix and sums the surviving entries.
--
-- KEYS[1]: rpm sorted-set key
-- KEYS[2]: tpm sorted-set key
-- ARGV[1]: now (ms, int)
-- ARGV[2]: window length (ms, int)
-- ARGV[3]: rpm limit (0 → unlimited)
-- ARGV[4]: tpm limit (0 → unlimited)
-- ARGV[5]: request_id (unique RPM member)
--
-- Returns:  {allowed, reject_code, rpm_used, rpm_oldest_ms, tpm_used}
--   allowed       1 = pass, 0 = reject
--   reject_code   0 = ok, 1 = rpm rejected, 2 = tpm rejected
--   rpm_used      post-add count if allowed; pre-add count if rejected
--   rpm_oldest_ms score of the oldest live RPM entry (or `now` if empty)
--   tpm_used      sum of tokens in the live TPM window

local rpm_key = KEYS[1]
local tpm_key = KEYS[2]
local now     = tonumber(ARGV[1])
local window  = tonumber(ARGV[2])
local rpm_lim = tonumber(ARGV[3])
local tpm_lim = tonumber(ARGV[4])
local req_id  = ARGV[5]

local function oldest_score(key)
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    if #oldest >= 2 then
        return tonumber(oldest[2])
    end
    return now
end

local ttl = math.ceil(window * 2 / 1000)

redis.call('ZREMRANGEBYSCORE', rpm_key, '-inf', now - window)
local rpm_count = redis.call('ZCARD', rpm_key)

if rpm_lim > 0 and rpm_count >= rpm_lim then
    return {0, 1, rpm_count, oldest_score(rpm_key), 0}
end

local tpm_total = 0
if tpm_lim > 0 then
    redis.call('ZREMRANGEBYSCORE', tpm_key, '-inf', now - window)
    local members = redis.call('ZRANGE', tpm_key, 0, -1)
    for _, m in ipairs(members) do
        local colon = string.find(m, ':[^:]*$')
        if colon then
            local n = tonumber(string.sub(m, colon + 1))
            if n then
                tpm_total = tpm_total + n
            end
        end
    end
    if tpm_total >= tpm_lim then
        return {0, 2, rpm_count, oldest_score(rpm_key), tpm_total}
    end
end

if rpm_lim > 0 then
    redis.call('ZADD', rpm_key, now, req_id)
    redis.call('EXPIRE', rpm_key, ttl)
end

return {1, 0, rpm_count + 1, oldest_score(rpm_key), tpm_total}
