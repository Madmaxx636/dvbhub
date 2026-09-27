#include <stdio.h>
#include <stddef.h>
#include <sys/ioctl.h>
#include <linux/dvb/frontend.h>
#include <linux/dvb/dmx.h>

#define P(x) printf("%-28s 0x%lx\n", #x, (unsigned long)(x))
int main(void) {
	P(FE_GET_INFO); P(FE_READ_STATUS); P(FE_SET_PROPERTY); P(FE_GET_PROPERTY);
	P(FE_SET_TONE); P(FE_SET_VOLTAGE); P(FE_DISEQC_SEND_MASTER_CMD); P(FE_DISEQC_SEND_BURST);
	P(DMX_START); P(DMX_STOP); P(DMX_SET_PES_FILTER); P(DMX_SET_BUFFER_SIZE); P(DMX_ADD_PID);
	P(sizeof(struct dtv_property)); P(offsetof(struct dtv_property, u));
	P(offsetof(struct dtv_property, result)); P(sizeof(struct dtv_properties));
	P(sizeof(struct dmx_pes_filter_params)); P(sizeof(struct dvb_frontend_info));
	P(DTV_STAT_SIGNAL_STRENGTH); P(DTV_STAT_CNR); P(DTV_STREAM_ID); P(DTV_ENUM_DELSYS);
	P(DTV_TRANSMISSION_MODE); P(DTV_GUARD_INTERVAL); P(DTV_HIERARCHY); P(DTV_CODE_RATE_HP);
	P(SYS_DVBT2); P(SYS_DVBC_ANNEX_A); P(SYS_DVBS2); P(SYS_ATSC);
	P(QAM_256); P(APSK_16); P(PSK_8); P(FEC_3_5); P(FEC_9_10); P(TRANSMISSION_MODE_32K);
	P(GUARD_INTERVAL_1_128); P(GUARD_INTERVAL_19_256); P(ROLLOFF_AUTO); P(PILOT_AUTO);
	P(DMX_OUT_TSDEMUX_TAP); P(DMX_PES_OTHER); P(DMX_IMMEDIATE_START);
	return 0;
}
