# Research: alarms, night light and sound on a LineageOS Echo Show

Researched 2026-11-01 for [#108](https://github.com/DeRusto/goCasa/issues/108) (map: [#107](https://github.com/DeRusto/goCasa/issues/107)). Devices: Echo Show 5 (2019, `checkers`) and Echo Show 8 (2019, `crown`) on R0rt1z2's unofficial LineageOS 18.1.

Already settled, not repeated here: screen-off, camera wake and the Home-app approach ([gocasa-android#4](https://github.com/DeRusto/gocasa-android/issues/4), [gocasa-android#13](https://github.com/DeRusto/gocasa-android/issues/13)). In short: the Display app is the default Home app, the Wall Activity uses `showWhenLocked` + `turnScreenOn`, and window brightness 0 is clamped to the panel minimum rather than turning the backlight off.

Sources are the porters' own device trees, kernel and HALs on GitHub (`amazon-oss` org), LineageOS repos, AOSP `android11-release`, Chromium source and developer.android.com. The XDA threads returned HTTP 403 to automated fetches, so their "what works" lists were **not read**. Anything marked **[HW]** needs a test on a real unit.

## Short answer

- **Platform:** both run **LineageOS 18.1 = Android 11 (API 30)** on a MediaTek **MT8163** with a **32-bit ARM userspace**. Latest builds: `checkers` v0.8 and `crown` v0.6, both 2026-10-14. SELinux is **permissive**.
- **WebView:** LineageOS's own Chromium WebView (`com.android.webview`, 32-bit arm), pulled from LineageOS's `main` prebuilt at build time. The 2026-10-14 builds should carry **Chromium 155.0.8059.39** (inferred from commit dates; **[HW]** confirm). It is the only allowed provider, so Google WebView/Chrome can't be selected. Sideloading an update works only if it is signed with the ROM's platform key. The practical update path is **flashing the next ROM release** (LineageOS currently ships a new WebView about every 2 weeks; 156.0.8078.47 is out).
- **Alarms: yes, with a large margin.** The device tree runs a `suspend_blocker` daemon that holds a kernel wakelock forever, so the SoC **never deep-sleeps** and timers can't be lost in suspend. On top of that, AOSP 11 gives `setAlarmClock()` `FLAG_WAKE_FROM_IDLE` and an App Standby exemption, and Android 11 needs no exact-alarm permission. None of this uses Play Services. Wake + over-lock-screen uses the settled Home-app + `showWhenLocked`/`turnScreenOn` path.
- **Night light: yes, through the screen.** The porters set `config_screenBrightnessSettingMinimum` to **1** (AOSP default 10), so a window brightness override of 0 lands at backlight step **1/255**. Combine that with a full-screen dark red/amber view in the app's own window. How dark step 1 really is on these panels is **[HW]**. **No usable LED:** the only LED node in the device tree is `lcd-backlight`, and the lights HAL only implements `BACKLIGHT`.
- **Sound: yes.** Use `MediaPlayer`/ExoPlayer in a `mediaPlayback` foreground service, with `USAGE_ALARM` for alarm tones (alarm stream, separate volume) and `USAGE_MEDIA` for white noise. The device never suspends, so hours of looping is fine as far as the platform goes. **WebView takes persistent audio focus once a page plays audible media** (Chromium `kRequestSystemAudioFocus` and `kDeferAudioFocusUntilAudible`, both on by default on Android), so native players must handle focus loss deliberately.
- **Gotcha:** the physical **mic-mute button is wired as `KEY_POWER`** in the device tree, so pressing it puts the display to sleep.

---

## 1. OS and WebView

### LineageOS / Android version

| | Echo Show 5 (2019) | Echo Show 8 (2019) |
|---|---|---|
| Codename | `checkers` | `crown` |
| ROM | LineageOS 18.1 (Android 11), unofficial, by R0rt1z2 | same |
| Latest release | [`lineage-18.1-checkers-v0.8`](https://github.com/amazon-oss/releases/releases/tag/lineage-18.1-checkers-v0.8) (2026-10-14) | [`lineage-18.1-crown-v0.6`](https://github.com/amazon-oss/releases/releases/tag/lineage-18.1-crown-v0.6) (2026-10-14) |
| XDA thread (porter) | [4763475](https://xdaforums.com/t/rom-unofficial-11-checkers-lineageos-18-1-for-the-amazon-echo-show-5-2019.4763475/) | [4766709](https://xdaforums.com/t/rom-unofficial-11-crown-lineageos-18-1-for-the-amazon-echo-show-8-2019.4766709/) |
| Panel (device.mk) | 960x480, 195 dpi | 1280x800, 213 dpi |
| `ro.config.low_ram` | `true` (Android Go, from common `vendor.prop`) | overridden to `false` in `device.mk` (**[HW]** check `getprop`) |

- Both inherit from [`android_device_amazon_mt8163-common`](https://github.com/amazon-oss/android_device_amazon_mt8163-common) (`TARGET_BOARD_PLATFORM := mt8163`). `BoardConfigCommon.mk` sets `TARGET_ARCH := arm` (32-bit userspace, arm64 kernel 4.9) and `androidboot.selinux=permissive`.
- The build manifest is [`amazon-oss/local_manifests` `amazon_mt8163.xml`](https://github.com/amazon-oss/local_manifests/blob/lineage-18.1/amazon_mt8163.xml) on top of LineageOS 18.1, and it does not override WebView.
- `PRODUCT_SHIPPING_API_LEVEL := 25` (Fire OS was Nougat-based). Device type is `common_full_tablet_wifionly` ([`lineage_checkers.mk`](https://github.com/amazon-oss/android_device_amazon_checkers/blob/lineage-18.1/lineage_checkers.mk)).
- LineageOS 18.1 is no longer an official LineageOS branch. Security patches come only from the porter's rebuilds.

### System WebView

- LineageOS 18.1's manifest ([`snippets/lineage.xml`](https://github.com/LineageOS/android/blob/lineage-18.1/snippets/lineage.xml)) pulls `external/chromium-webview/prebuilt/arm` from [`LineageOS/android_external_chromium-webview_prebuilt_arm`](https://github.com/LineageOS/android_external_chromium-webview_prebuilt_arm) at **`revision="main"`**. So every fresh 18.1 build picks up the current WebView, even on an EOL branch.
- On `main`, the arm prebuilt went to 155.0.8059.39 on 2026-10-09 and to 156.0.8078.47 on 2026-10-22. The 2026-10-14 device builds should therefore have **155.0.8059.39**. This is inferred, so confirm with `adb shell dumpsys webviewupdate`.
- [`os_pickup.mk`](https://github.com/LineageOS/android_external_chromium-webview_patches/blob/main/os_pickup.mk) installs it as package `webview` with `LOCAL_CERTIFICATE := $(DEFAULT_SYSTEM_DEV_CERTIFICATE)`, so it is re-signed with the build's platform key.
- LineageOS 18.1's [`config_webview_packages.xml`](https://github.com/LineageOS/android_frameworks_base/blob/lineage-18.1/core/res/res/xml/config_webview_packages.xml) lists **only `com.android.webview`**. `com.google.android.webview` and Chrome aren't valid providers, and the Developer options "WebView implementation" picker can't offer them.

**Can it be updated by sideloading?** Only as an update to the system `com.android.webview`, which Android accepts only when the APK is signed with the same key as the system copy.
- If the porter signs with AOSP **test-keys** (common for unofficial builds; check `getprop ro.build.tags`), you can take LineageOS's newer `webview.apk`, sign it with the public AOSP testkey and `adb install -r` it. **[HW]**
- If the porter uses private release keys, this is impossible without the porter.
- Either way the supported path is to **flash the next ROM release**. For goCasa, assume a Chromium ~155 WebView that stays current only as often as the porter rebuilds.

## 2. Alarms

### The device never suspends

- [`suspend_blocker/suspend_blocker_mt8163.cpp`](https://github.com/amazon-oss/android_device_amazon_mt8163-common/blob/lineage-18.1/suspend_blocker/suspend_blocker_mt8163.cpp) creates a `WakeLock{"suspend_blocker_mt8163"}` and then calls `sigsuspend()` forever. Both device makefiles add it (`suspend_blocker_mt8163`), and it starts as `class hal` ([`.rc`](https://github.com/amazon-oss/android_device_amazon_mt8163-common/blob/lineage-18.1/suspend_blocker/suspend_blocker_mt8163.rc)). The kernel therefore never enters suspend, and RTC/hrtimer wakeups can't be missed. The common overlay also sets `config_powerDecoupleAutoSuspendModeFromDisplay=false`.
- `vendor.prop` sets `ro.vendor.power.wall_powered=true`. [`init.mt8163.power.rc`](https://github.com/amazon-oss/android_device_amazon_mt8163-common/blob/lineage-18.1/init/init.mt8163.power.rc) uses it to turn off hotplug, keep all 4 cores online and use the `performance` governor. So no CPU power saving gets in the way of timers.

### Framework Doze still applies, and `setAlarmClock()` is exempt from it

Kernel suspend and Android Doze are separate things. Doze (`DeviceIdleController`) is a framework state that defers alarms, jobs and network.
- AOSP 11 `DeviceIdleController` enters idle only when `!mCharging`, with charging taken from `ACTION_BATTERY_CHANGED` as `present && plugged` ([source](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/android11-release/apex/jobscheduler/service/java/com/android/server/DeviceIdleController.java)). The Echo Show has no battery, so whether the health HAL reports "not charging" (and Doze can engage) is **[HW]**. Check with `adb shell dumpsys deviceidle` and `dumpsys battery`. It doesn't matter for alarm clocks:
- AOSP 11 `AlarmManagerService.setImpl`: `if (alarmClock != null) { flags |= AlarmManager.FLAG_WAKE_FROM_IDLE | AlarmManager.FLAG_STANDALONE; ...`. `isExemptFromAppStandby()` returns true for `a.alarmClock != null` ([source](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/android11-release/services/core/java/com/android/server/AlarmManagerService.java)). Doze also schedules around an upcoming alarm clock (`isUpcomingAlarmClock()` in `DeviceIdleController`).
- developer.android.com: the system exits Doze shortly before a `setAlarmClock()` alarm fires ([Optimize for Doze and App Standby](https://developer.android.com/training/monitoring-device-state/doze-standby)).
- **Permissions:** on Android 11 `setAlarmClock()` needs none. `SCHEDULE_EXACT_ALARM`/`USE_EXACT_ALARM` start at API 31 ([AlarmManager](https://developer.android.com/reference/android/app/AlarmManager#setAlarmClock(android.app.AlarmManager.AlarmClockInfo,%20android.app.PendingIntent)), [exact alarms](https://developer.android.com/develop/background-work/services/alarms/schedule#exact)). Declare `SCHEDULE_EXACT_ALARM` anyway in case a later port moves to a newer Android.
- **Play Services:** not involved. `AlarmManager` is pure framework.
- **Reboot:** alarms are cleared on reboot, so re-register them from `BOOT_COMPLETED` (or when the Home activity first starts). LineageOS has no Fire OS update reboots, but power cuts happen.

### Wake the screen and ring over the lock screen

- Point the alarm's `PendingIntent` at the app (an Activity, or a receiver that calls `startActivity()`). The settled design applies as is: the default Home app is exempt from background-activity-start limits, and an activity with `showWhenLocked` + `turnScreenOn` turns the screen on over the keyguard ([gocasa-android#13](https://github.com/DeRusto/gocasa-android/issues/13)). Start the sound from a foreground service (§4) so it doesn't depend on the activity being visible.
- `AlarmClockInfo` also shows the next alarm in SystemUI and makes `AlarmManager.getNextAlarmClock()` return it, which costs nothing.
- **[HW]:** end-to-end fire time with the screen off via `lockNow()` (expect under 1 s, since the SoC is awake); that it still fires after a long `dumpsys deviceidle force-idle`; and that sound starts while the screen comes on.

## 3. Night light and Ok-to-Wake

### Backlight floor

- Both device overlays ([checkers](https://github.com/amazon-oss/android_device_amazon_checkers/blob/lineage-18.1/overlays/FrameworksResOverlayCheckers/res/values/config.xml), [crown](https://github.com/amazon-oss/android_device_amazon_crown/blob/lineage-18.1/overlays/FrameworksResOverlayCrown/res/values/config.xml)) set `config_screenBrightnessSettingMinimum = 1` and auto-brightness LCD values down to `1` below 5 lux. The AOSP 11 defaults are `config_screenBrightnessSettingMinimum` 10 and `config_screenBrightnessDim` 10 ([config.xml](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/android11-release/core/res/res/values/config.xml)).
- AOSP 11 `DisplayPowerController`: `mScreenBrightnessRangeMinimum = Math.min(screenBrightnessSettingMinimumFloat, mScreenBrightnessDimConfig)`, and a window override (`screenBrightnessOverride`) is constrained to `[mScreenBrightnessRangeMinimum, max]` ([source](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/android11-release/services/core/java/com/android/server/display/DisplayPowerController.java)). So `WindowManager.LayoutParams.screenBrightness = 0f` gives **brightness 1/255**, not the AOSP floor of 10/255. No permission is needed, and it applies only while the app's window is in front. That suits the Wall.
- Lights HAL [`hardware/amazon/lights/Light.cpp`](https://github.com/amazon-oss/android_hardware_amazon/blob/lineage-18.1/lights/Light.cpp) scales 1..255 linearly onto `/sys/class/leds/lcd-backlight/max_brightness` and writes it, and **0 stays 0**. The kernel DT backlight node is `mediatek,lcd-backlight` with `led_mode = <5>` (MediaTek BLS PWM) ([`checkers.dtsi`](https://github.com/amazon-oss/android_kernel_amazon_mt8163/blob/lineage-18.1/arch/arm64/boot/dts/mediatek/checkers.dtsi), [`crown.dtsi`](https://github.com/amazon-oss/android_kernel_amazon_mt8163/blob/lineage-18.1/arch/arm64/boot/dts/mediatek/crown.dtsi)).
- **[HW]:** how bright PWM step 1 really is, whether it flickers, and whether the panel still shows a usable tint at that level. Read `/sys/class/leds/lcd-backlight/max_brightness` and `brightness` while testing.

### Coloured light

- **Do it in the app's own window.** Draw a full-screen solid colour (deep red/amber for night, green for "OK to get up") in the Wall/night Activity (or WebView page), together with `screenBrightness = 0f`. Black pixels on an IPS/LCD panel still leak backlight, so a dark red fill at step 1 is the dimmest coloured result. Pure black is a poor night light, not "off". Real off is `lockNow()` (settled).
- Dimming further with alpha is free: a red at e.g. 20% luminance on top of backlight step 1 multiplies down. Banding at very low values on the cheap panels (Show 5's ST7701S, Show 8's JD936x) is **[HW]**.
- **System-wide options are not needed and mostly unavailable:** AOSP Night Light depends on `config_nightDisplayAvailable` = `config_setColorTransformAccelerated` (default false, not overridden here). LineageOS LiveDisplay's `DisplayColorCalibration` writes an RGB gain sysfs file ([livedisplay](https://github.com/amazon-oss/android_device_amazon_mt8163-common/tree/lineage-18.1/livedisplay)), but that is system-only and doesn't make anything dimmer. A `TYPE_APPLICATION_OVERLAY` tint over other apps would need `SYSTEM_ALERT_WINDOW`, which is pointless when the Display app owns the screen.

### LED

- **None usable.** The kernel DTs define one LED node, `led@6` `mediatek,lcd-backlight`. The lights HAL's `backends` list is `{ LightType::BACKLIGHT }`, and every other id returns `EX_UNSUPPORTED_OPERATION`. `init.mt8163.rc` chowns `/sys/class/leds/{red,green,blue}/trigger`, but that is generic MediaTek boilerplate with no DT node behind it.
- The Echo's own mute indicator is outside Android control (not found in DT or HAL). The mute button itself is `gpio-keys` `label = "mute"`, `linux,code = <116>`, `wakeup-source`, and 116 is `KEY_POWER` ([input-event-codes.h](https://github.com/torvalds/linux/blob/master/include/uapi/linux/input-event-codes.h)). So **pressing mic-mute sleeps/wakes the display**. Fine for a bedside "screen off" button, but surprising if a child presses it expecting only the mic to mute. **[HW]** whether it also cuts the mic in hardware.
- The camera shutter is a switch on `gpio-keys` that the porters' `EchoParts` handles ([`init.echoparts.rc`](https://github.com/amazon-oss/android_hardware_amazon/blob/lineage-18.1/EchoParts/init.echoparts.rc)).
- The ambient light sensor works (`BOARD_LIGHT_SENSOR_SUPPORT := true`, `sensors.amazon`, `android.hardware.sensor.light`). The app could read lux to fade the night light or decide the room is dark. **[HW]** accuracy at very low lux.

## 4. Sound

### Audio stack

- Speaker output only (plus BT and wired). The audio HAL is the porters' `audio.primary.amazon_wrapper` around Amazon's blob `audio.primary_amazon.mt8163.so` (`device.mk`, `proprietary-files.txt`, [`hardware/amazon/audio/hal`](https://github.com/amazon-oss/android_hardware_amazon/tree/lineage-18.1/audio/hal)). `audio_policy_configuration.xml` has one `primary output` mix port and a `Speaker` device port.
- Known issue on `crown`: there is no working echo canceller, so mic captures the speaker at full level. The porters ship an optional AEC shim ([jxlarrea/lineageos-echo-show-camera](https://github.com/jxlarrea/lineageos-echo-show-camera)). This affects calls and voice, not playback.
- **[HW]:** maximum loudness and distortion at full alarm volume through the wrapper HAL (Amazon's EQ/MBCL tuning blobs are loaded from `vendor/etc/audio-algorithms`, so tuning should match Fire OS), and pops at stream start or stop.

### Playback design

- **Alarm tone:** `AudioAttributes.USAGE_ALARM` (`CONTENT_TYPE_SONIFICATION`). It plays on `STREAM_ALARM` with its own volume, and DND lets alarms through by default ([AudioAttributes](https://developer.android.com/reference/android/media/AudioAttributes#USAGE_ALARM)). Request `AUDIOFOCUS_GAIN_TRANSIENT` while ringing. Set the alarm volume from the app with `AudioManager.setStreamVolume(STREAM_ALARM, ...)`, which needs no permission except when it would change DND state. Ramp volume in the player (`setVolume`) for a gentle wake.
- **White noise for hours:** `USAGE_MEDIA` in a **foreground service** with `android:foregroundServiceType="mediaPlayback"` and `FOREGROUND_SERVICE` (normal), plus a partial wake lock (`MediaPlayer.setWakeMode(..., PARTIAL_WAKE_LOCK)`). It is technically redundant here because of `suspend_blocker`, but it keeps the app correct on other hardware. [Foreground service types](https://developer.android.com/develop/background-work/services/fgs/service-types#media).
  - On `checkers` (`low_ram=true`, about 1 GB RAM) a foreground service is what stops LMK from killing the player once the screen is off.
  - For **gapless looping**, use a WAV/PCM or Ogg Vorbis asset with `MediaPlayer.setLooping(true)`, or Media3/ExoPlayer `REPEAT_MODE_ONE`. MP3/AAC encoder padding leaves an audible gap at every loop. Or generate noise in an `AudioTrack` loop (no file, no gap). **[HW]** an 8-hour soak for underruns and thermal behaviour on the MT8163 with all cores pinned at `performance`.
- **Volume steps:** expose them in the app UI. The Echo's hardware volume buttons drive the "current" stream through the framework as on any Android device (**[HW]** that the buttons map to `KEY_VOLUMEUP/DOWN`).

### Audio focus alongside the WebView

- WebView disables only the JS MediaSession *API* (`switches::kDisableMediaSessionAPI` in [`aw_main_delegate.cc`](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.39/android_webview/lib/aw_main_delegate.cc)). Content-layer audio focus still runs: [`media_switches.cc`](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.39/media/base/media_switches.cc) has `kRequestSystemAudioFocus` and `kDeferAudioFocusUntilAudible` both enabled by default on Android. [`audio_focus_delegate_android.cc`](https://chromium.googlesource.com/chromium/src/+/refs/tags/155.0.8059.39/content/browser/media/session/audio_focus_delegate_android.cc) then requests **persistent `AUDIOFOCUS_GAIN`** from Android once the page becomes audible.
- Consequences:
  1. Any `<audio>`/`<video>` or Web Audio playback in the Wall page takes focus from native white noise, and a well-behaved native player pauses. Either keep all sound native (recommended: the page asks the app to play over the JS bridge and never plays audio itself), or have the white-noise player ignore focus loss on purpose.
  2. When the native alarm takes `GAIN_TRANSIENT`, Chromium's media session should pause or duck the page's media and resume afterwards. **[HW]** check that it resumes.
- Short UI sounds in the page (Web Audio beeps) also count as "audible", so they'd interrupt white noise too. That is one more reason to route all sound through the app.

## Hardware test checklist

1. `getprop ro.build.version.release`, `ro.build.tags`, `ro.config.low_ram`, and `dumpsys webviewupdate` on each model.
2. `dumpsys battery` / `dumpsys deviceidle`: is the device "charging"? Does Doze ever engage?
3. `setAlarmClock()` 2 minutes ahead, screen off via `lockNow()`, after `dumpsys deviceidle force-idle`: does the Wall come up over the keyguard with sound?
4. Night light: `screenBrightness = 0f` + dark red full-screen view in a dark room. Read `/sys/class/leds/lcd-backlight/brightness`, check flicker and banding.
5. Mic-mute button: confirm it acts as power and whether the hardware mic cut and indicator are separate.
6. 8-hour white-noise loop: gaps, underruns, temperature. Then play audio in the WebView and watch the native player's focus callbacks.
7. Alarm loudness at max `STREAM_ALARM` on both models.
