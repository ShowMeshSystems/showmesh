import { useState } from 'react'
import type { Model } from '../api'
import { Button, ButtonRow, ChromeStop, ConfirmDialog, Drawer, Notice } from '../kit'
import {
  EMERGENCY_LEVEL_LABEL,
  STOP_CONFIRM_DETAIL,
  STOP_CONFIRM_TITLE,
  STOP_POWER_DOWN_CONFIRM_DETAIL,
  STOP_POWER_DOWN_CONFIRM_TITLE,
  useEmergencyStop,
} from './emergencyStop'
import { EmergencyStopOutcome } from './EmergencyStopOutcome'

/**
 * The emergency stop on the top bar, on every screen. The first button asks
 * before the level 1 stop; the second opens the other two levels and the outcome.
 */
export function EmergencyStopChrome({ model }: { model: Model }) {
  const emergency = useEmergencyStop(model)
  const { gate, busy, hardStopArm, armRemainingMs, armExpired } = emergency
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [powerDownConfirmOpen, setPowerDownConfirmOpen] = useState(false)
  const [panelOpen, setPanelOpen] = useState(false)

  // An arm nobody can see is not left standing: closing the popup drops it.
  const closePanel = () => {
    emergency.cancelArm()
    setPanelOpen(false)
  }

  return (
    <>
      <ChromeStop
        stopLabel="Emergency stop"
        onStop={() => setConfirmOpen(true)}
        moreLabel="More emergency stop levels"
        onMore={() => (panelOpen ? closePanel() : setPanelOpen(true))}
        moreExpanded={panelOpen}
        disabledReason={gate.allowed ? undefined : gate.reason}
      />
      <ConfirmDialog
        open={confirmOpen}
        title={STOP_CONFIRM_TITLE}
        detail={gate.allowed ? STOP_CONFIRM_DETAIL : gate.reason}
        confirmLabel="Stop"
        confirmDisabled={!gate.allowed || busy !== false}
        onConfirm={() => {
          setConfirmOpen(false)
          emergency.stop()
          setPanelOpen(true)
        }}
        onCancel={() => setConfirmOpen(false)}
      />
      <ConfirmDialog
        open={powerDownConfirmOpen}
        title={STOP_POWER_DOWN_CONFIRM_TITLE}
        detail={gate.allowed ? STOP_POWER_DOWN_CONFIRM_DETAIL : gate.reason}
        confirmLabel="Stop and power down"
        confirmDisabled={!gate.allowed || busy !== false}
        onConfirm={() => {
          setPowerDownConfirmOpen(false)
          emergency.stopPowerDown()
        }}
        onCancel={() => setPowerDownConfirmOpen(false)}
      />
      <Drawer open={panelOpen} onClose={closePanel} labelledBy="chrome-estop-heading">
        <div className="sm-inspector">
          <h3 className="sm-inspector__title" id="chrome-estop-heading">Emergency stop</h3>
          {!gate.allowed && <p className="sm-small sm-muted">{gate.reason}</p>}
          <div className="sm-lc-emergency">
            <p className="sm-small sm-muted">Stop, plus an active night session goes straight to its power-down.</p>
            <ButtonRow>
              <Button variant="danger" size="gloved" disabled={!gate.allowed || busy !== false} onClick={() => setPowerDownConfirmOpen(true)}>
                Stop and power down
              </Button>
            </ButtonRow>
            <div className="sm-lc-emergency__hardstop">
              <p className="sm-small sm-muted">Stop and power down, plus an active night session ends at once. Arm, then fire.</p>
              <ButtonRow>
                <Button size="gloved" disabled={!gate.allowed || emergency.hardStopArmBusy} onClick={emergency.armHardStop}>
                  Arm hard stop
                </Button>
                <Button
                  variant="danger"
                  size="gloved"
                  disabled={!gate.allowed || hardStopArm === null || armExpired}
                  onClick={emergency.fireHardStop}
                >
                  Fire hard stop
                </Button>
                {hardStopArm !== null && (
                  <Button variant="quiet" size="gloved" onClick={emergency.cancelArm}>
                    Cancel
                  </Button>
                )}
              </ButtonRow>
              {emergency.hardStopArmError !== null && <Notice tone="bad" headline={`Arm was refused: ${emergency.hardStopArmError}`} />}
              {hardStopArm !== null && armRemainingMs !== null && (
                <Notice
                  tone="warn"
                  live="status"
                  headline={
                    armExpired
                      ? 'The arm token expired. Arm again, then fire promptly.'
                      : `Armed. Fire within ${Math.max(0, Math.ceil(armRemainingMs / 1000))}s, or arm again to reset the window.`
                  }
                />
              )}
            </div>
            {busy !== false && (
              <p className="sm-small sm-muted" role="status">
                {EMERGENCY_LEVEL_LABEL[busy]} sent. Waiting for the coordinator to report what stopped.
              </p>
            )}
            <EmergencyStopOutcome outcome={emergency.outcome} />
          </div>
        </div>
      </Drawer>
    </>
  )
}
