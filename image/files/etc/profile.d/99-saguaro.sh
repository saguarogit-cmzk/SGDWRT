# Saguaro — podsjetnik na konzoli.
#
# Čovjek koji prvi put digne uređaj ne zna odakle početi, a sučelje mu je na
# adresi koja gotovo nikad ne odgovara njegovoj mreži. Zato mu to piše odmah
# pri prijavi na konzolu.

_sag_lan=$(ip -4 addr show br-lan 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1 | head -1)

# Zadana lozinka se ispisuje samo dok je stvarno na snazi (health javlja
# default_password). Poslije promjene bi podsjetnik bio dezinformacija i curio
# bi lozinku svakome tko pogleda ekran.
_sag_defpw=$(wget -q -O - --no-check-certificate https://127.0.0.1:8443/api/v1/health 2>/dev/null \
	| grep -o '"default_password":true')

printf '\n'
printf '  Saguaro Infrastructure\n'
if [ -n "$_sag_lan" ]; then
	if [ -n "$_sag_defpw" ]; then
		printf '  Sučelje:  https://%s:8443/   (admin / Sgs#2026)\n' "$_sag_lan"
	else
		printf '  Sučelje:  https://%s:8443/\n' "$_sag_lan"
	fi
else
	printf '  Sučelje:  mreža još nije podignuta\n'
fi
printf '  Postavljanje s konzole (adresa, instalacija na disk):  saguaro-setup\n\n'

unset _sag_lan _sag_defpw
