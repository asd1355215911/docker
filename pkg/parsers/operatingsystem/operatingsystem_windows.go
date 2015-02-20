package operatingsystem

// TODO Windows. Appropriate Win32 calls

func GetOperatingSystem() (string, error) {
	return "Windows 10 - GetOperatingSystem() needs implementing", nil
}

// No-op on Windows
func IsContainerized() (bool, error) {
	return false, nil
}
