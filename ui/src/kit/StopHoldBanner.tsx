/**
 * The shell-level, non-dismissible banner shown on every screen while an
 * emergency Stop holds the night session. It carries no button: Resume lives
 * on Live Control and Show Night. The caller owns every word.
 */
export function StopHoldBanner({ message, detail }: { message: string; detail?: string }) {
  return (
    <div className="sm-wdbanner" role="region" aria-label="Show stopped">
      <div className="sm-wdbanner__fact">
        <span className="sm-wdbanner__kind" role="alert">
          {message}
        </span>
        {detail !== undefined && <span>{detail}</span>}
      </div>
    </div>
  )
}
