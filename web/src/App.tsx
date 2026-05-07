import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import Home from './pages/Home'
import NewProject from './pages/NewProject'
import CuePicker from './pages/CuePicker'
import Assembly from './pages/Assembly'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/projects/new" element={<NewProject />} />
        <Route path="/projects/:id/cues" element={<CuePicker />} />
        <Route path="/projects/:id/assembly" element={<Assembly />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
