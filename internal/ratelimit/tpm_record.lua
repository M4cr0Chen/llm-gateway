-- tpm_record.lua: append a token-usage entry to the TPM sorted set.
-- The member encodes the token count as a ":N" suffix; tpm_check.lua
-- parses it back out during the sliding sum.
--
-- KEYS[1]: tpm sorted-set key
-- ARGV[1]: now (unix seconds, float)
-- ARGV[2]: window length in seconds
-- ARGV[3]: request_id (unique member prefix)
-- ARGV[4]: tokens to record

local key    = KEYS[1]
local now    = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local req_id = ARGV[3]
local tokens = ARGV[4]

redis.call('ZADD', key, now, req_id .. ':' .. tokens)
redis.call('EXPIRE', key, math.ceil(window * 2))
return 1
