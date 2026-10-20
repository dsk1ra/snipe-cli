package phase1

import "testing"

func TestWorkdayAPI(t *testing.T) {
	for in, want := range map[string]string{
		"https://barclays.wd3.myworkdayjobs.com/External_Career_Site_Barclays/job/Canary-Wharf/Senior-AI-Engineer_JR-0000117488-1": "https://barclays.wd3.myworkdayjobs.com/wday/cxs/barclays/External_Career_Site_Barclays/job/Canary-Wharf/Senior-AI-Engineer_JR-0000117488-1",
		"https://acme.wd5.myworkdayjobs.com/en-GB/External/job/London/Engineer_R1?source=linkedin":                                 "https://acme.wd5.myworkdayjobs.com/wday/cxs/acme/External/job/London/Engineer_R1",
		"https://wd3.myworkdaysite.com/recruiting/bsigroup/BSI_Careers/job/Taiwan/Auditor_JR0020765":                               "https://wd3.myworkdaysite.com/wday/cxs/bsigroup/BSI_Careers/job/Taiwan/Auditor_JR0020765",
		"https://acme.wd3.myworkdayjobs.com/External":                                                                              "",
		"https://jobs.lever.co/acme/1/job/x":                                                                                       "",
	} {
		got, ok := workdayAPI(in)
		if got != want || ok != (want != "") {
			t.Errorf("workdayAPI(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}
