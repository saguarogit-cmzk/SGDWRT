# Saguaro — podsjetnik na konzoli.
#
# Čovjek koji prvi put digne uređaj ne zna odakle početi, a sučelje mu je na
# adresi koja gotovo nikad ne odgovara njegovoj mreži. Zato mu to piše odmah
# pri prijavi na konzolu.

_sag_lan=$(ip -4 addr show br-lan 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1 | head -1)

printf '\n'
printf '  Saguaro Infrastructure\n'
if [ -n "$_sag_lan" ]; then
	printf '  Sučelje:  https://%s:8443/   (admin / Sgs#2026)\n' "$_sag_lan"
else
	printf '  Sučelje:  mreža još nije podignuta\n'
fi
printf '  Postavljanje s konzole (adresa, instalacija na disk):  saguaro-setup\n\n'

unset _sag_lan
