#!/bin/sh
# Example for PWM hardware where duty 500 is full speed and 3000 is minimum.
# Configure this absolute path as fan_script in LuCI, or copy and customize it.
set -eu

speed=${1:?missing fan percentage}
case "$speed" in
	''|*[!0-9]*) exit 2 ;;
esac
[ "$speed" -le 100 ] || exit 2

chip=/sys/class/pwm/pwmchip0
pwm="$chip/pwm0"
[ -d "$chip" ] || exit 1
[ -d "$pwm" ] || echo 0 > "$chip/export"
[ -d "$pwm" ] || exit 1

if [ "$(cat "$pwm/enable")" != 1 ]; then
	echo 10000 > "$pwm/period"
	echo normal > "$pwm/polarity"
fi

duty=$((3000 - speed * 25))
echo "$duty" > "$pwm/duty_cycle"
echo 1 > "$pwm/enable"
