// Integration with the systemd D-Bus API.  See http://www.freedesktop.org/wiki/Software/systemd/dbus/
package dbus

import (
	"github.com/guelfey/go.dbus"
	"sync"
	"time"
)

const signalBuffer = 100
const managerInterface = "org.freedesktop.systemd1.Manager"

type subscriberT struct {
	jobs     map[dbus.ObjectPath]chan string
	jobsLock sync.Mutex
}

var subscriber subscriberT

var sysconn *dbus.Conn
var sysobj *dbus.Object

func init() {
	var err error
	sysconn, err = dbus.SystemBusPrivate()
	if err != nil {
		return
	}

	err = sysconn.Auth(nil)
	if err != nil {
		sysconn.Close()
		return
	}

	err = sysconn.Hello()
	if err != nil {
		sysconn.Close()
		return
	}

	sysobj = sysconn.Object("org.freedesktop.systemd1", dbus.ObjectPath("/org/freedesktop/systemd1"))

	sysconn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0,
		"type='signal',path='/org/freedesktop/systemd1'")
	err = sysobj.Call("org.freedesktop.systemd1.Manager.Subscribe", 0).Store()
	if err != nil {
		sysconn.Close()
		return
	}

	initSubscriber(&subscriber)
}

func initSubscriber(s *subscriberT) {
	s.jobs = make(map[dbus.ObjectPath]chan string)
	ch := make(chan *dbus.Signal, signalBuffer)

	sysconn.Signal(ch)

	go func() {
		for {
			signal := <-ch
			if signal == nil {
				continue
			}
			switch signal.Name {
			case managerInterface + ".JobRemoved":
				var id uint32
				var job dbus.ObjectPath
				var unit string
				var result string
				dbus.Store(signal.Body, &id, &job, &unit, &result)
				s.jobsLock.Lock()
				out, ok := s.jobs[job]
				if ok {
					out <- result
				}
				s.jobsLock.Unlock()
			}
		}
	}()
}

func startJob(job string, args ...interface{}) (<-chan string, error) {
	subscriber.jobsLock.Lock()
	defer subscriber.jobsLock.Unlock()

	ch := make(chan string, 1)
	var path dbus.ObjectPath
	err := sysobj.Call(job, 0, args...).Store(&path)
	if err != nil {
		return nil, err
	}
	subscriber.jobs[path] = ch
	return ch, nil
}

func runJob(job string, args ...interface{}) (string, error) {
	respCh, err := startJob(job, args...)
	if err != nil {
		return "", err
	}
	return <-respCh, nil
}

// StartUnit enqeues a start job and possibly depending jobs.
//
// Takes the unit to activate, plus a mode string. The mode needs to be one of
// replace, fail, isolate, ignore-dependencies, ignore-requirements. If
// "replace" the call will start the unit and its dependencies, possibly
// replacing already queued jobs that conflict with this. If "fail" the call
// will start the unit and its dependencies, but will fail if this would change
// an already queued job. If "isolate" the call will start the unit in question
// and terminate all units that aren't dependencies of it. If
// "ignore-dependencies" it will start a unit but ignore all its dependencies.
// If "ignore-requirements" it will start a unit but only ignore the
// requirement dependencies. It is not recommended to make use of the latter
// two options.
//
// Result string: one of done, canceled, timeout, failed, dependency, skipped.
// done indicates successful execution of a job. canceled indicates that a job
// has been canceled  before it finished execution. timeout indicates that the
// job timeout was reached. failed indicates that the job failed. dependency
// indicates that a job this job has been depending on failed and the job hence
// has been removed too. skipped indicates that a job was skipped because it
// didn't apply to the units current state.
func StartUnit(name string, mode string) (string, error) {
	return runJob("StartUnit", name, mode)
}

// StopUnit is similar to StartUnit but stops the specified unit rather
// than starting it.
func StopUnit(name string, mode string) (string, error) {
	return runJob("StopUnit", name, mode)
}

// ReloadUnit reloads a unit.  Reloading is done only if the unit is already running and fails otherwise.
func ReloadUnit(name string, mode string) (string, error) {
	return runJob("ReloadUnit", name, mode)
}

// RestartUnit restarts a service.  If a service is restarted that isn't
// running it will be started.
func RestartUnit(name string, mode string) (string, error) {
	return runJob("RestartUnit", name, mode)
}

// TryRestartUnit is like RestartUnit, except that a service that isn't running
// is not affected by the restart.
func TryRestartUnit(name string, mode string) (string, error) {
	return runJob("TryRestartUnit", name, mode)
}

// ReloadOrRestart attempts a reload if the unit supports it and use a restart
// otherwise.
func ReloadOrRestartUnit(name string, mode string) (string, error) {
	return runJob("ReloadOrRestartUnit", name, mode)
}

// ReloadOrTryRestart attempts a reload if the unit supports it and use a "Try"
// flavored restart otherwise.
func ReloadOrTryRestartUnit(name string, mode string) (string, error) {
	return runJob("ReloadOrTryRestartUnit", name, mode)
}

// StartTransientUnit() may be used to create and start a transient unit, which
// will be released as soon as it is not running or referenced anymore or the
// system is rebooted. name is the unit name including suffix, and must be
// unique. mode is the same as in StartUnit(), properties contains properties
// of the unit.
func StartTransientUnit(name string, mode string, properties ...Property) (string, error) {
	return runJob("StartTransientUnit", name, mode, properties, make(auxT, 0))
}

// KillUnit takes the unit name and a UNIX signal number to send.  All of the unit's
// processes are killed.
func KillUnit(name string, signal int32) {
	sysobj.Call("KillUnit", 0, name, "all", signal).Store()
}

// ListUnits returns an array with all currently loaded units. Note that
// units may be known by multiple names at the same time, and hence there might
// be more unit names loaded than actual units behind them.
func ListUnits() ([]UnitStatus, error) {
	result := make([][]interface{}, 0)
	err := sysobj.Call("ListUnits", 0).Store(&result)
	if err != nil {
		return nil, err
	}

	resultInterface := make([]interface{}, len(result))
	for i := range result {
		resultInterface[i] = result[i]
	}

	status := make([]UnitStatus, len(result))
	statusInterface := make([]interface{}, len(status))
	for i := range status {
		statusInterface[i] = &status[i]
	}

	err = dbus.Store(resultInterface, statusInterface...)
	if err != nil {
		return nil, err
	}

	return status, nil
}

type UnitStatus struct {
	Name        string          // The primary unit name as string
	Description string          // The human readable description string
	LoadState   string          // The load state (i.e. whether the unit file has been loaded successfully)
	ActiveState string          // The active state (i.e. whether the unit is currently started or not)
	SubState    string          // The sub state (a more fine-grained version of the active state that is specific to the unit type, which the active state is not)
	Followed    string          // A unit that is being followed in its state by this unit, if there is any, otherwise the empty string.
	Path        dbus.ObjectPath // The unit object path
	JobId       uint32          // If there is a job queued for the job unit the numeric job id, 0 otherwise
	JobType     string          // The job type as string
	JobPath     dbus.ObjectPath // The job object path
}

// Returns two unbuffered channels which will receive all changed units every
// @interval@ seconds.  Deleted units are sent as nil.
func SubscribeUnits(interval time.Duration) (<-chan map[string]*UnitStatus, <-chan error) {
	return SubscribeUnitsCustom(interval, 0, func(u1, u2 *UnitStatus) bool { return *u1 != *u2 })
}

// SubscribeUnitsCustom is like SubscribeUnits but lets you specify the buffer
// size of the channels and the comparison function for detecting changes.
func SubscribeUnitsCustom(interval time.Duration, buffer int, isChanged func(*UnitStatus, *UnitStatus) bool) (<-chan map[string]*UnitStatus, <-chan error) {
	old := make(map[string]*UnitStatus)
	statusChan := make(chan map[string]*UnitStatus, buffer)
	errChan := make(chan error, buffer)

	go func() {
		for {
			timerChan := time.After(interval)

			units, err := ListUnits()
			if err == nil {
				cur := make(map[string]*UnitStatus)
				for i := range units {
					cur[units[i].Name] = &units[i]
				}

				// add all new or changed units
				changed := make(map[string]*UnitStatus)
				for n, u := range cur {
					if oldU, ok := old[n]; !ok || isChanged(oldU, u) {
						changed[n] = u
					}
					delete(old, n)
				}

				// add all deleted units
				for oldN := range old {
					changed[oldN] = nil
				}

				old = cur

				statusChan <- changed
			} else {
				errChan <- err
			}

			<-timerChan
		}
	}()

	return statusChan, errChan
}
