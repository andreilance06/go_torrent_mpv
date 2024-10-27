-- Utils
local msg = require("mp.msg")
local utils = require("mp.utils")
local options = require("mp.options")

-- Constants
local TORRENT_PATTERNS = { "%.torrent$", "^magnet:%?xt=urn:btih:", "^http[s]?://", "^" .. string.rep("%x", 40) .. "$" }
local EXCLUDE_PATTERNS = { "127%.0%.0%.1", "192%.168%.%d+%.%d+", "/torrents/" }

-- State management
local State = {
  client_running = false,
  launched_by_us = false,
  torrents = {},
}

-- Configuration
local Config = {
  opts = {
    DisableUTP = true,
    DownloadDir = os.getenv("tmp"),
    MaxConnsPerTorrent = 200,
    Port = 6969,
    Readahead = 32 * 1024 * 1024,
    Responsive = false,
    ResumeTorrents = true,
    Profiling = false,
    startClientOnMpvLaunch = true,
    closeClientOnMpvExit = true,
    closeClientOnNoTorrentFiles = false
  }
}

function Config.load()
  options.read_options(Config.opts)
  return Config
end

function Config.get_client_args()
  local args = {}
  for i, v in pairs(Config.opts) do
    local first_char = i:sub(1, 1)
    if string.upper(first_char) == first_char then
      args[#args + 1] = "--" .. i .. "=" .. tostring(v)
    end
  end
  return args
end

-- Client management
local Client = {}
function Client.is_running()
  local cmd = mp.command_native({
    name = "subprocess",
    playback_only = false,
    capture_stdout = true,
    capture_stderr = true,
    args = { "curl", "-s", "--connect-timeout", "0.25", "localhost:" .. Config.opts.Port .. "/torrents" }
  })
  return cmd.status == 0
end

function Client.update_status()
  if State.client_running then
    local cmd = mp.command_native({
      name = "subprocess",
      playback_only = false,
      capture_stdout = true,
      capture_stderr = true,
      args = { "curl", "-s", "--connect-timeout", "5", "localhost:" .. Config.opts.Port .. "/torrents" }
    })

    if cmd.status ~= 0 then
      return nil
    end

    local t = utils.parse_json(cmd.stdout)
    State.torrents = {}
    for _, v in pairs(t) do
      State.torrents[v.InfoHash] = { Name = v.Name, Files = v.Files, Length = v.Length, Playlist = v.Playlist }
    end
  end
end

function Client.start()
  if not State.client_running then
    if Client.is_running() then
      msg.debug("Client is already running")
      State.client_running = true
      return true
    end

    local res = mp.command_native({
      name = "subprocess",
      playback_only = false,
      capture_stderr = true,
      args = { mp.get_script_directory() .. "/go_torrent_mpv.exe", table.unpack(Config.get_client_args()) },
      detach = true
    })

    if res.status ~= 0 then
      msg.debug("Failed to start client:", res.stderr)
      return false
    end

    msg.debug("Started torrent server")
    State.client_running = true
    State.launched_by_us = true
    return true
  end
  return true
end

function Client.close()
  if State.client_running and State.launched_by_us then
    mp.command_native({
      name = "subprocess",
      playback_only = false,
      capture_stderr = true,
      args = { "curl", "localhost:" .. Config.opts.Port .. "/exit" }
    })
    msg.debug("Closed torrent server")
    State.client_running = false
    State.launched_by_us = false
  end
end

-- Torrent operations
local TorrentOps = {}
function TorrentOps.add(torrent_url)
  if not State.client_running then
    msg.error("Server must be online to add torrents")
    return nil
  end

  local playlist_req = mp.command_native({
    name = "subprocess",
    capture_stdout = true,
    args = { "curl", "-s", "--retry", "10", "--retry-delay", "1", "--retry-connrefused", "-d",
      torrent_url, "localhost:" .. Config.opts.Port .. "/torrents" }
  })

  local playlist = playlist_req.stdout
  if not playlist or #playlist == 0 then
    msg.debug("Unable to get playlist for", torrent_url)
    return nil
  end

  return playlist
end

function TorrentOps.remove(info_hash, delete_files)
  if not State.client_running then
    msg.error("Server must be online to remove torrents")
    return false
  end

  if not State.torrents[info_hash] then
    msg.error("Torrent", info_hash, "does not exist")
    return false
  end

  if delete_files == nil then
    delete_files = false
  end

  mp.command_native({
    name = "subprocess",
    playback_only = false,
    args = { "curl", "-X", "DELETE", "localhost:" .. Config.opts.Port .. "/torrents/" .. info_hash .. "?DeleteFiles=" .. tostring(delete_files) },
  })
  State.torrents[info_hash] = nil
  return true
end

-- Menu integration
local Menu = {}
function Menu.create_torrent_menu(menu_id, index)
  local menu_items = {}
  local to_update
  Client.update_status()

  -- Add client control items
  table.insert(menu_items, {
    title = "Client Controls",
    items = {
      {
        title = State.client_running and "Stop Client" or "Start Client",
        icon = State.client_running and "stop" or "play_arrow",
        value = State.client_running and "client_stop" or "client_start"
      }
    }
  })

  if State.client_running then
    local remove_torrents_submenu = {}
    for i, v in pairs(State.torrents) do
      table.insert(remove_torrents_submenu, {
        title = v.Name,
        hint = string.format("%.1f GB", v.Length / (1024 * 1024 * 1024)),
        value = i,
        actions = {
          { name = "delete",       icon = "delete",         label = "Delete torrent" },
          { name = "delete_files", icon = "delete_forever", label = "Delete torrent & files" }
        }
      })
    end

    if next(State.torrents) ~= nil then
      -- Append to previous item (Client Controls)
      table.insert(menu_items[#menu_items].items, {
        title = "Remove Torrent",
        items = remove_torrents_submenu
      })
    end

    -- Add items for each torrent
    for _, v in pairs(State.torrents) do
      local submenu_items = {}
      if #v.Files > 1 then
        table.insert(submenu_items, {
          title = "Play all",
          value = v.Playlist,
          actions = {
            { name = "play_all",        icon = "playlist_play", label = "Play all files" },
            { name = "play_all_append", icon = "playlist_add",  label = "Append all files to playlist" }
          }
        })
      end
      for _, file in pairs(v.Files) do
        table.insert(submenu_items, {
          title = file.Name,
          hint = string.format("%.1f MB", file.Length / (1024 * 1024)),
          icon = mp.get_property("stream-open-filename", "") == file.URL and "music_note" or nil,
          value = file.URL,
          actions = {
            { name = "play_file",   icon = "play_circle_outline", label = "Play file" },
            { name = "play_append", icon = "add_to_queue",        label = "Queue" },
            { name = "play_next",   icon = "queue_play_next",     label = "Queue and play next" }
          }
        })
      end

      table.insert(menu_items, {
        title = v.Name,
        hint = string.format("%d files", #v.Files),
        items = submenu_items
      })
    end
  end

  if menu_id and index then
    for _, v in pairs(menu_items) do
      if v.title == menu_id then
        to_update = v.items[index]
        break
      end
    end
  end

  return {
    type = "torrent_menu",
    title = "Torrent Manager",
    items = menu_items,
    callback = { mp.get_script_name(), "menu-callback" }
  }, to_update
end

function Menu.show()
  local menu_data = Menu.create_torrent_menu()
  mp.commandv("script-message-to", "uosc", "open-menu", utils.format_json(menu_data))
end

function Menu.update(menu_id, index)
  local menu_data, to_update = Menu.create_torrent_menu(menu_id, index)
  if to_update then
    return to_update, function()
      mp.commandv("script-message-to", "uosc", "update-menu", utils.format_json(menu_data))
    end
  end
  mp.commandv("script-message-to", "uosc", "update-menu", utils.format_json(menu_data))
end

function Menu.handle_callback(json_event)
  local event = utils.parse_json(json_event)
  if event.type == "activate" then
    if event.value == "client_start" then
      local item, done = Menu.update(event.menu_id, event.index)
      item.value = "noop"
      item.icon = "spinner"
      done()
      Client.start()
      Menu.update()
    elseif event.value == "client_stop" then
      local item, done = Menu.update(event.menu_id, event.index)
      item.value = "noop"
      item.icon = "spinner"
      done()
      Client.close()
      Menu.update()
    elseif event.action == "play_all" then
      mp.commandv("loadfile", "memory://" .. event.value)
      mp.commandv("script-message-to", "uosc", "close-menu", "torrent_menu")
    elseif event.action == "play_all_append" then
      mp.commandv("loadfile", "memory://" .. event.value, "append")
      local item, done = Menu.update(event.menu_id, event.index)
      item.actions[2].name = "noop"
      item.actions[2].icon = "check"
      done()
      mp.add_timeout(0.5, function()
        Menu.update()
      end)
    elseif event.action == "play_file" then
      mp.commandv("loadfile", event.value)
      mp.commandv("script-message-to", "uosc", "close-menu", "torrent_menu")
    elseif event.action == "play_append" then
      mp.commandv("loadfile", event.value, "append")
      local item, done = Menu.update(event.menu_id, event.index)
      item.actions[2].name = "noop"
      item.actions[2].icon = "check"
      done()
      mp.add_timeout(0.5, function()
        Menu.update()
      end)
    elseif event.action == "play_next" then
      mp.commandv("loadfile", event.value, "insert-next")
      local item, done = Menu.update(event.menu_id, event.index)
      item.actions[3].name = "noop"
      item.actions[3].icon = "check"
      done()
      mp.add_timeout(0.5, function()
        Menu.update()
      end)
    elseif event.action == "delete" then
      local item, done = Menu.update(event.menu_id, event.index)
      item.actions = {}
      item.icon = "spinner"
      done()
      TorrentOps.remove(event.value, false)
      Menu.update()
    elseif event.action == "delete_files" then
      local item, done = Menu.update(event.menu_id, event.index)
      item.actions = {}
      item.icon = "spinner"
      done()
      TorrentOps.remove(event.value, true)
      Menu.update()
    end
  end
end

-- Event handlers
local function on_file_loaded()
  local path = mp.get_property("stream-open-filename", "")

  for _, pattern in ipairs(EXCLUDE_PATTERNS) do
    if path:find(pattern) then return end
  end

  for _, pattern in ipairs(TORRENT_PATTERNS) do
    if path:find(pattern) then
      if Client.start() then
        local playlist = TorrentOps.add(path)
        if playlist then
          Client.update_status()
          mp.set_property("stream-open-filename", "memory://" .. playlist)
          return
        end
      end
      break
    end
  end

  if next(State.torrents) == nil and Config.opts.closeClientOnNoTorrentFiles then
    Client.close()
  end
end

-- Script initialization
local function init()
  Config.load()

  -- Register menu command
  mp.add_key_binding("Alt+t", "toggle-torrent-menu", function()
    Menu.show()
  end)

  -- Register menu callback handler
  mp.register_script_message("menu-callback", function(json)
    Menu.handle_callback(json)
  end)

  -- Register MPV event handlers
  mp.add_hook("on_load", 50, on_file_loaded)

  if Config.opts.closeClientOnMpvExit then
    mp.register_event("shutdown", function() Client.close() end)
  end

  if Config.opts.startClientOnMpvLaunch then
    Client.start()
  end
end

init()
