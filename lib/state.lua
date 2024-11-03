local msg = require("mp.msg")
local utils = require("mp.utils")

local Config = require("lib/config")

local State = {
  client_running = false,
  launched_by_us = false,
  torrents = {},
}

function State.is_running()
  local cmd = mp.command_native({
    name = "subprocess",
    playback_only = false,
    capture_stdout = true,
    capture_stderr = true,
    args = { "curl", "-s", "--connect-timeout", "0.25", "localhost:" .. Config.opts.Port .. "/torrents" }
  })
  return cmd.status == 0
end

function State.update()
  State.torrents = {}
  if not State.client_running then
    return false
  end

  local cmd = mp.command_native({
    name = "subprocess",
    playback_only = false,
    capture_stdout = true,
    capture_stderr = true,
    args = { "curl", "-s", "--connect-timeout", "5", "localhost:" .. Config.opts.Port .. "/torrents" }
  })

  if cmd.status ~= 0 then
    State.client_running = false
    msg.error("error updating client state: subprocess status is", cmd.status)
    return false
  end

  local t = utils.parse_json(cmd.stdout)
  for _, v in pairs(t) do
    State.torrents[v.InfoHash] = {
      Name = v.Name,
      Files = v.Files,
      Length = v.Length,
      Playlist = v.Playlist,
      MimeType = v.MimeType
    }
  end

  return true
end

return State
